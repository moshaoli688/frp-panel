package proxy

import (
	"fmt"

	"github.com/VaalaCat/frp-panel/biz/master/client"
	"github.com/VaalaCat/frp-panel/common"
	"github.com/VaalaCat/frp-panel/defs"
	"github.com/VaalaCat/frp-panel/models"
	"github.com/VaalaCat/frp-panel/pb"
	"github.com/VaalaCat/frp-panel/services/app"
	"github.com/VaalaCat/frp-panel/services/dao"
	"github.com/VaalaCat/frp-panel/utils"
	"github.com/VaalaCat/frp-panel/utils/logger"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
	"github.com/samber/lo"
)

func UpdateProxyConfig(c *app.Context, req *pb.UpdateProxyConfigRequest) (*pb.UpdateProxyConfigResponse, error) {
	if len(req.GetClientId()) == 0 || len(req.GetServerId()) == 0 || len(req.GetConfig()) == 0 {
		return nil, fmt.Errorf("request invalid")
	}

	var (
		userInfo = common.GetUserInfo(c)
		clientID = req.GetClientId()
		serverID = req.GetServerId()
	)

	cli, err := dao.NewQuery(c).GetClientByClientID(userInfo, clientID)
	if err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot get client, id: [%s]", clientID)
		return nil, err
	}

	clientEntity := cli.ClientEntity

	if clientEntity.ServerID != serverID {
		logger.Logger(c).Errorf("client and server not match, find or create client, client: [%s], server: [%s]", clientID, serverID)
		originClient, err := dao.NewQuery(c).GetClientByClientID(userInfo, clientEntity.OriginClientID)
		if err != nil {
			logger.Logger(c).WithError(err).Errorf("cannot get origin client, id: [%s]", clientEntity.OriginClientID)
			return nil, err
		}

		clientEntity, _, err = client.ChildClientForServer(c, serverID, originClient.ClientEntity)
		if err != nil {
			logger.Logger(c).WithError(err).Errorf("cannot create child client, id: [%s]", clientID)
			return nil, err
		}
	}

	_, err = dao.NewQuery(c).GetServerByServerID(userInfo, serverID)
	if err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot get server, id: [%s]", serverID)
		return nil, err
	}

	proxyCfg := &models.ProxyConfigEntity{}
	if err := proxyCfg.FillClientConfig(clientEntity); err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot fill client config, id: [%s]", clientID)
		return nil, err
	}

	typedProxyCfgs, err := utils.LoadProxiesFromContent(req.GetConfig())
	if err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot load proxies from content")
		return nil, err
	}
	if len(typedProxyCfgs) == 0 || len(typedProxyCfgs) > 1 {
		logger.Logger(c).Errorf("invalid config, cfg len: [%d]", len(typedProxyCfgs))
		return nil, fmt.Errorf("invalid config")
	}

	typedProxyCfg := typedProxyCfgs[0]

	if typedProxyCfg.GetBaseConfig().Type == string(v1.ProxyTypeHTTP) {
		typedProxyCfg = UpdateWorkerLoadBalancerGroup(typedProxyCfg)
	}

	if err := proxyCfg.FillTypedProxyConfig(typedProxyCfg); err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot fill typed proxy config")
		return nil, err
	}

	oldProxyCfg, err := dao.NewQuery(c).GetProxyConfigByOriginClientIDAndName(userInfo, clientID, proxyCfg.Name)
	if err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot get proxy config, id: [%s]", clientID)
		return nil, err
	}

	if dao.NewQuery(c).UpdateProxyConfig(userInfo, &models.ProxyConfig{
		Model:             oldProxyCfg.Model,
		ProxyConfigEntity: proxyCfg,
	}) != nil {
		logger.Logger(c).Errorf("update proxy config failed, cfg: [%+v]", proxyCfg)
		return nil, fmt.Errorf("update proxy config failed")
	}

	// update client config
	if oldCfg, err := clientEntity.GetConfigContent(); err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot get client config, id: [%s]", clientID)
		return nil, err
	} else {
		oldCfg.Proxies = lo.Filter(oldCfg.Proxies, func(proxy v1.TypedProxyConfig, _ int) bool {
			return proxy.GetBaseConfig().Name != typedProxyCfg.GetBaseConfig().Name
		})
		oldCfg.Proxies = append(oldCfg.Proxies, typedProxyCfg)

		if err := clientEntity.SetConfigContent(*oldCfg); err != nil {
			logger.Logger(c).WithError(err).Errorf("cannot set client config, id: [%s]", clientID)
			return nil, err
		}
	}

	rawCfg, err := clientEntity.MarshalJSONConfig()
	if err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot marshal client config, id: [%s]", clientID)
		return nil, err
	}

	_, err = client.UpdateFrpcHander(c, &pb.UpdateFRPCRequest{
		ClientId: &clientEntity.ClientID,
		ServerId: &serverID,
		Config:   rawCfg,
		Comment:  &clientEntity.Comment,
		FrpsUrl:  &clientEntity.FrpsUrl,
	})
	if err != nil {
		logger.Logger(c).WithError(err).Errorf("cannot update frpc, id: [%s]", clientID)
		return nil, err
	}

	return &pb.UpdateProxyConfigResponse{
		Status: &pb.Status{Code: pb.RespCode_RESP_CODE_SUCCESS, Message: "ok"},
	}, nil
}

func UpdateWorkerLoadBalancerGroup(typedProxyCfg v1.TypedProxyConfig) v1.TypedProxyConfig {
	annotations := typedProxyCfg.GetBaseConfig().Annotations
	workerId := ""
	if len(annotations) > 0 {
		if annotations[defs.FrpProxyAnnotationsKey_Ingress] != "" && len(annotations[defs.FrpProxyAnnotationsKey_WorkerId]) > 0 {
			workerId = annotations[defs.FrpProxyAnnotationsKey_WorkerId]
		}
	}
	httpProxyCfg := &v1.HTTPProxyConfig{}
	msg := &msg.NewProxy{}
	typedProxyCfg.ProxyConfigurer.MarshalToMsg(msg)
	httpProxyCfg.UnmarshalFromMsg(msg)

	if len(workerId) > 0 {
		httpProxyCfg.LoadBalancer = v1.LoadBalancerConfig{
			Group:    models.HttpIngressLBGroup(workerId, httpProxyCfg),
			GroupKey: workerId,
		}
	}

	typedProxyCfg.ProxyConfigurer = httpProxyCfg

	return typedProxyCfg
}
