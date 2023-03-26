package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

type MyOpcUaClient struct {
	*opcua.Client
	Interval time.Duration
	FromDB   chan Message
	ToDB     chan Message
	db       Dao
	IdCache  map[string]*ua.NodeID
}

func NewMyOpcUaClient(ctx context.Context, config OpcUaConfig, dbToOpc chan Message, opcToDb chan Message, db Dao) *MyOpcUaClient {
	endpoints, err := opcua.GetEndpoints(ctx, config.Endpoint)
	if err != nil {
		log.Fatal(err)
	}
	ep := opcua.SelectEndpoint(endpoints, config.Policy, ua.MessageSecurityModeFromString(config.Mode))
	if ep == nil {
		log.Fatal("Failed to find suitable endpoint")
	}

	fmt.Println("*", ep.SecurityPolicyURI, ep.SecurityMode)

	opts := []opcua.Option{
		opcua.SecurityPolicy(config.Policy),
		opcua.SecurityModeString(config.Mode),
		opcua.CertificateFile(config.CertFile),
		opcua.PrivateKeyFile(config.KeyFile),
		opcua.AuthAnonymous(),
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeAnonymous),
	}

	return &MyOpcUaClient{
		Client:   opcua.NewClient(ep.EndpointURL, opts...),
		Interval: config.Interval,
		FromDB:   dbToOpc,
		ToDB:     opcToDb,
		db:       db,
		IdCache:  make(map[string]*ua.NodeID),
	}
}

func (c *MyOpcUaClient) findOrParse(nodeId string) (*ua.NodeID, error) {
	if id, ok := c.IdCache[nodeId]; ok {
		return id, nil
	}

	id, err := ua.ParseNodeID(nodeId)
	if err != nil {
		return nil, err
	}

	c.IdCache[nodeId] = id

	return id, nil
}

func (c *MyOpcUaClient) write(ctx context.Context, nodeIds []string, values []interface{}) {
	for i, nodeId := range nodeIds {
		id, err := c.findOrParse(nodeId)
		if err != nil {
			log.Fatalf("invalid node id: %v", err)
		}

		v, err := ua.NewVariant(values[i])
		if err != nil {
			log.Fatalf("invalid value: %v", err)
		}

		req := &ua.WriteRequest{
			NodesToWrite: []*ua.WriteValue{
				{
					NodeID:      id,
					AttributeID: ua.AttributeIDValue,
					Value: &ua.DataValue{
						EncodingMask: ua.DataValueValue,
						Value:        v,
					},
				},
			},
		}

		resp, err := c.WriteWithContext(ctx, req)
		if err != nil {
			log.Fatalf("Write failed: %s", err)
		}

		for _, result := range resp.Results {
			_ = result
			// log.Printf("%v", result)
		}
	}
}

func (c *MyOpcUaClient) read(ctx context.Context, nodeIds []string) {
	nodesToRead := make([]*ua.ReadValueID, 0)

	for _, nodeId := range nodeIds {
		id, err := c.findOrParse(nodeId)
		if err != nil {
			log.Fatalf("invalid node id: %v", err)
		}

		nodesToRead = append(nodesToRead, &ua.ReadValueID{
			AttributeID: ua.AttributeIDValue,
			NodeID:      id,
		})
	}

	req := &ua.ReadRequest{
		MaxAge:             2000,
		NodesToRead:        nodesToRead,
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	}

	resp, err := c.ReadWithContext(ctx, req)
	if err != nil {
		log.Fatalf("Read failed: %s", err)
	}

	for i, result := range resp.Results {
		if result.Status != ua.StatusOK {
			log.Fatalf("Status not OK: %v", result.Status)
		}
		log.Printf("%v = %#v (%v)", nodesToRead[i].NodeID, result.Value.Value(), result.Value.Type().String())
	}
}

func (c *MyOpcUaClient) subscribe(ctx context.Context, nodeIds []string) error {
	notifyCh := make(chan *opcua.PublishNotificationData)

	sub, err := c.SubscribeWithContext(ctx, &opcua.SubscriptionParameters{
		Interval: c.Interval,
	}, notifyCh)
	if err != nil {
		return fmt.Errorf("subscription creation was failed: %w", err)
	}

	log.Printf("Created subscription with id %v", sub.SubscriptionID)

	IdMap := make(map[uint32]*ua.NodeID)

	for _, nodeID := range nodeIds {
		id, err := c.findOrParse(nodeID)
		if err != nil {
			return fmt.Errorf("node id parsing was failed: %w", err)
		}

		handle := uuid.New().ID()
		IdMap[handle] = id

		var miCreateRequest *ua.MonitoredItemCreateRequest
		miCreateRequest = c.valueRequest(id, handle)
		res, err := sub.Monitor(ua.TimestampsToReturnBoth, miCreateRequest)
		if err != nil || res.Results[0].StatusCode != ua.StatusOK {
			if err == nil {
				err = errors.New(res.Results[0].StatusCode.Error())
			}
			return fmt.Errorf("monitored item creation request was failed:%w", err)
		}
	}

	c.db.InitVariables(nodeIds)

	c.db.SetIsReady()

	// read from subscription's notification channel until ctx is cancelled
	for {
		select {
		case <-ctx.Done():
			return errors.New("other goroutine was ended")
		case res := <-notifyCh:
			if res.Error != nil {
				log.Printf("subscription response error: %q", res.Error)
				continue
			}

			switch x := res.Value.(type) {
			case *ua.DataChangeNotification:
				nodeIds := make([]string, 0, len(x.MonitoredItems))
				values := make([]string, 0, len(x.MonitoredItems))
				for _, item := range x.MonitoredItems {
					id := IdMap[item.ClientHandle]
					data := item.Value.Value.Value()
					nodeIds = append(nodeIds, id.String())
					values = append(values, fmt.Sprint(data))
					// log.Printf("MonitoredItem with client handle %v(%v) = %v", id, item.ClientHandle, data)
				}

				c.ToDB <- &UpdateVariablesMessage{
					NodeIds: nodeIds,
					Values:  values,
				}

			default:
				log.Printf("what's this publish result? %T", res.Value)
			}
		}
	}
}

func (c *MyOpcUaClient) close(ctx context.Context) {
	log.Println("--- close sequence executed ---")
	// if err := c.CloseSession(); err != nil {
	// 	log.Printf("--- close session error: %v", err)
	// }
	if err := c.CloseWithContext(ctx); err != nil {
		log.Printf("--- close opcua error: %v", err)
	}
}

func (c *MyOpcUaClient) valueRequest(nodeID *ua.NodeID, handle uint32) *ua.MonitoredItemCreateRequest {
	return opcua.NewMonitoredItemCreateRequestWithDefaults(nodeID, ua.AttributeIDValue, handle)
}

func (c *MyOpcUaClient) runOpcUa(ctx context.Context) error {
	wg := new(sync.WaitGroup)
	defer wg.Wait()

	subctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			select {
			case <-subctx.Done():
				return
			case msgif := <-c.FromDB:
				switch msg := msgif.(type) {
				case *WriteControlMessage:
					nodeIDsToWrite := []string{
						msg.NodeId,
					}

					values := []interface{}{
						msg.Value,
					}

					c.write(ctx, nodeIDsToWrite, values)
				}
			}

			select {
			case <-subctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}

		/*
			nodeIDsToWrite := []string{
				"ns=2;s=Root.Code",
				"ns=2;s=Root.Count",
				"ns=2;s=Root.Message",
				"ns=2;s=Root.Timestamp",
				"ns=2;s=Root.Value",
			}

			values := []interface{}{
				float32(1.234),
				float32(2.345),
				float32(3.456),
			}

			for {
				_ = nodeIDsToWrite
				// c.write(ctx, nodeIDsToWrite, values)

				for i := 0; i < len(values); i++ {
					values[i] = values[i].(float32) + float32(0.1)
				}

				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
		*/
	}()

	nodeIDs := []string{
		"ns=2;s=1:CC1001?Output",
	}

	// nodeIDs := []string{
	// 	"ns=2;s=Root.Code",
	// 	"ns=2;s=Root.Count",
	// 	"ns=2;s=Root.Message",
	// 	"ns=2;s=Root.Timestamp",
	// 	"ns=2;s=Root.Value",
	// }

	/*
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.read(ctx, nodeIDs)
		}()
	*/

	err := c.subscribe(ctx, nodeIDs)
	if err != nil {
		return fmt.Errorf("subscription failed: %w", err)
	}

	return nil
}
