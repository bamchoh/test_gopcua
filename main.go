// Copyright 2018-2020 opcua authors. All rights reserved.
// Use of this source code is governed by a MIT-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/debug"

	_ "github.com/mattn/go-sqlite3"
)

type MessageType int

const (
	InitVariables = iota
	WriteControl
	UpdateVariable
	IsReady
)

type Message interface {
	Type() MessageType
}

type InitVariablesMessage struct {
	NodeIds []string
}

func (m *InitVariablesMessage) Type() MessageType {
	return InitVariables
}

type WriteControlMessage struct {
	NodeId string
	Value  uint16
}

func (m *WriteControlMessage) Type() MessageType {
	return WriteControl
}

type UpdateVariablesMessage struct {
	NodeIds []string
	Values  []string
}

func (m *UpdateVariablesMessage) Type() MessageType {
	return UpdateVariable
}

type IsReadyMessage struct{}

func (m *IsReadyMessage) Type() MessageType {
	return IsReady
}

type OpcUaConfig struct {
	Endpoint string
	Policy   string
	Mode     string
	CertFile string
	KeyFile  string
	Interval time.Duration
}

func parseFlags() OpcUaConfig {
	var (
		endpoint = flag.String("endpoint", "opc.tcp://localhost:4840", "OPC UA Endpoint URL")
		policy   = flag.String("policy", "None", "Security policy: None, Basic128Rsa15, Basic256, Basic256Sha256. Default: auto")
		mode     = flag.String("mode", "", "Security mode: None, Sign, SignAndEncrypt. Default: auto")
		certFile = flag.String("cert", "", "Path to cert.pem. Required for security mode/policy != None")
		keyFile  = flag.String("key", "", "Path to private key.pem. Required for security mode/policy != None")
		interval = flag.Duration("interval", opcua.DefaultSubscriptionInterval, "subscription interval")
	)
	flag.BoolVar(&debug.Enable, "debug", false, "enable debug logging")
	flag.Parse()

	return OpcUaConfig{
		Endpoint: *endpoint,
		Policy:   *policy,
		Mode:     *mode,
		CertFile: *certFile,
		KeyFile:  *keyFile,
		Interval: *interval,
	}
}

func main() {
	db, err := createSql()
	if err != nil {
		log.Fatalf("db creation was failed: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := new(sync.WaitGroup)
	defer wg.Wait()

	dbToOpc := make(chan Message, 1)

	opcToDb := make(chan Message, 1)

	config := parseFlags()
	log.SetFlags(0)

	wg.Add(1)
	go func() {
		defer wg.Done()

		c := NewMyOpcUaClient(ctx, config, dbToOpc, opcToDb, db)
		if err := c.Connect(ctx); err != nil {
			err = fmt.Errorf("opc ua connection was failed: %w", err)
			log.Println(err)
		}
		defer c.close(context.Background())

		err = c.runOpcUa(ctx)
		if err != nil {
			err = fmt.Errorf("runOPCUa failed: %w", err)
			log.Println(err)
		}
		cancel()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = db.Main(ctx, dbToOpc, opcToDb)
		if err != nil {
			err = fmt.Errorf("dbMain error: %w", err)
			log.Println(err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		srv := &WebServer{db}
		srv.Start(":1323", ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

		select {
		case s := <-sig:
			fmt.Printf("Signal received: %s \n", s.String())
			cancel()
		case <-ctx.Done():
			fmt.Println("Signal waiting goroutine: other goroutine was ended")
		}
	}()
}
