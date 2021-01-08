// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT License was not distributed with this
// file, you can obtain one at https://opensource.org/licenses/MIT.
//
// Copyright (c) DUSK NETWORK. All rights reserved.

package main

import (
	"net"
	"time"
)

// CmgrConfig is the config file for the node connection manager
type CmgrConfig struct {
	Port     string
	OnAccept func(net.Conn)
	OnConn   func(net.Conn, string) // takes the connection  and the string
}

type connmgr struct {
	CmgrConfig
}

//NewConnMgr creates a new connection manager
func newConnMgr(cfg CmgrConfig) *connmgr {
	cnnmgr := &connmgr{
		cfg,
	}

	go func() {
		addrPort := ":" + cfg.Port
		listener, err := net.Listen("tcp", addrPort)
		if err != nil {
			log.WithError(err).Error("newConnMgr will panic after listener returns err")
			panic(err)
		}

		defer func() {
			_ = listener.Close()
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				log.WithField("process", "connection manager").
					WithError(err).
					Warnln("error accepting connection request")
				continue
			}

			go cfg.OnAccept(conn)
		}
	}()

	return cnnmgr
}

// Connect dials a connection with its string, then on succession
// we pass the connection and the address to the OnConn method
func (c *connmgr) Connect(addr string) error {
	conn, err := c.Dial(addr)
	if err != nil {
		return err
	}

	if c.CmgrConfig.OnConn != nil {
		go c.CmgrConfig.OnConn(conn, addr)
	}

	return nil
}

// Dial dials up a connection, given its address string
func (c *connmgr) Dial(addr string) (net.Conn, error) {
	dialTimeout := 1 * time.Second
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
