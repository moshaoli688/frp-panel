package clientrpc

import (
	"context"
	"io"
	"time"

	"github.com/VaalaCat/frp-panel/pb"
	"github.com/VaalaCat/frp-panel/services/app"
	"github.com/VaalaCat/frp-panel/utils/logger"
	"github.com/google/uuid"
)

// func clientHandleServerSend(req *pb.ServerMessage) *pb.ClientMessage {
// 	logger.Logger(c).Infof("client get a server message, origin is: [%+v]", req)
// 	return &pb.ClientMessage{
// 		Event:     pb.Event_EVENT_DATA,
// 		ClientId:  req.ClientId,
// 		SessionId: req.SessionId,
// 		Data:      req.Data,
// 	}
// }

func registClientToMaster(appInstance app.Application, recvStream pb.Master_ServerSendClient, event pb.Event, clientID, clientSecret string) {
	ctx := context.Background()
	logger.Logger(ctx).Infof("start to regist client to master")
	for {
		err := recvStream.Send(&pb.ClientMessage{
			Event:     event,
			ClientId:  clientID,
			SessionId: uuid.New().String(),
			Secret:    clientSecret,
		})
		if err != nil {
			logger.Logger(ctx).WithError(err).Warnf("cannot send, sleep 3s and retry")
			time.Sleep(3 * time.Second)
			continue
		}

		resp, err := recvStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			logger.Logger(ctx).Fatalf("cannot receive %v", err)
		}

		if resp.GetEvent() == event {
			logger.Logger(ctx).Infof("client get server register envent success, clientID: %s", resp.GetClientId())
			break
		}
	}
}

func runCLientRpcHandler(appInstance app.Application, recvStream pb.Master_ServerSendClient, done chan bool, clientID string,
	clientHandleServerSend func(appInstance app.Application, req *pb.ServerMessage) *pb.ClientMessage) {
	c := context.Background()
	for {
		select {
		case <-done:
			logger.Logger(c).Infof("finish rpc client")
			recvStream.CloseSend()
			return
		default:
			resp, err := recvStream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				logger.Logger(context.Background()).WithError(err).Errorf("cannot receive, sleep 3s and return")
				time.Sleep(3 * time.Second)
				return
			}
			if resp == nil {
				continue
			}
			go func() {
				defer func() {
					if err := recover(); err != nil {
						logger.Logger(c).Errorf("catch panic, err: %v", err)
					}
				}()
				msg := clientHandleServerSend(appInstance, resp)
				if msg == nil {
					return
				}
				msg.ClientId = clientID
				msg.SessionId = resp.SessionId
				recvStream.Send(msg)
				logger.Logger(c).Infof("client resp received: %s", resp.GetClientId())
			}()
		}
	}
}

func startClientRpcHandler(appInstance app.Application, client app.MasterClient, done chan bool, clientID, clientSecret string, event pb.Event,
	clientHandleServerSend func(appInstance app.Application, req *pb.ServerMessage) *pb.ClientMessage) {
	c := context.Background()
	logger.Logger(c).Infof("start to run rpc client")
	for {
		select {
		case <-done:
			logger.Logger(c).Infof("finish rpc client")
			return
		default:
			recvStream, err := client.Call().ServerSend(context.Background())
			if err != nil {
				logger.Logger(context.Background()).WithError(err).Errorf("cannot recv, sleep 3s and retry")
				time.Sleep(3 * time.Second)
				continue
			}

			registClientToMaster(appInstance, recvStream, event, clientID, clientSecret)
			runCLientRpcHandler(appInstance, recvStream, done, clientID, clientHandleServerSend)
		}
	}
}
