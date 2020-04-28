package tests

import (
	"fmt"
	"github.com/dusk-network/dusk-protobuf/autogen/go/node"
	"github.com/dusk-network/dusk-protobuf/autogen/go/rusk"
	"google.golang.org/grpc"
	"net"
)

func StartMockServer(address string) {
	s := grpc.NewServer()
	rusk.RegisterRuskServer(s, &rusk.RuskMock{})
	node.RegisterWalletServer(s, &node.WalletMock{})
	node.RegisterTransactorServer(s, &node.TransactorMock{})

	if address == "" {
		address = "127.0.0.1:8080"
	}
	lis, _ := net.Listen("tcp", address)
	go func() {
		if err := s.Serve(lis); err != nil {
			panic(fmt.Sprintf("Server exited with error: %v", err))
		}
	}()
}
