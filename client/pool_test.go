package client_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/OpenBazaar/multiwallet/client"
	"github.com/OpenBazaar/multiwallet/model"
	"github.com/OpenBazaar/multiwallet/model/mock"
	"github.com/OpenBazaar/multiwallet/test"
	"github.com/OpenBazaar/multiwallet/test/factory"
	httpmock "gopkg.in/jarcoal/httpmock.v1"
)

func TestServerRotation(t *testing.T) {
	var (
		endpointOne = "http://localhost:8332"
		endpointTwo = "http://localhost:8336"
		p, err      = client.NewClientPool([]string{endpointOne, endpointTwo}, nil)
		txid        = "1be612e4f2b79af279e0b307337924072b819b3aca09fcb20370dd9492b83428"
		testPath    = func(host string) string { return fmt.Sprintf("%s/tx/%s", host, txid) }
		expectedTx  = factory.NewTransaction()
	)
	if err != nil {
		t.Fatal(err)
	}

	mockedHTTPClient := http.Client{}
	httpmock.ActivateNonDefault(&mockedHTTPClient)
	defer httpmock.DeactivateAndReset()
	for _, c := range p.ClientCache {
		c.HTTPClient = mockedHTTPClient
	}
	p.HTTPClient = mockedHTTPClient

	mock.MockWebsocketClientOnClientPool(p)

	httpmock.RegisterResponder(http.MethodGet, testPath(endpointOne),
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(http.StatusOK, expectedTx)
		},
	)

	tx, err := p.CurrentClient().GetTransaction(txid)
	if err != nil {
		t.Fatal(err)
	}
	test.ValidateTransaction(*tx, expectedTx, t)

	// Test invalid response, server rotation, then valid response from second server
	httpmock.Reset()
	httpmock.RegisterResponder(http.MethodGet, testPath(endpointOne),
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(http.StatusInternalServerError, expectedTx)
		},
	)

	httpmock.RegisterResponder(http.MethodGet, testPath(endpointTwo),
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(http.StatusOK, expectedTx)
		},
	)

	tx, err = p.CurrentClient().GetTransaction(txid)
	if err != nil {
		t.Fatal(err)
	}
	test.ValidateTransaction(*tx, expectedTx, t)
}

func TestClientPool_BlockNotify(t *testing.T) {
	var (
		p, err   = client.NewClientPool([]string{"http://localhost:8332", "http://localhost:8336"}, nil)
		testHash = "0000000000000000003f1fb88ac3dab0e607e87def0e9031f7bea02cb464a04f"
	)
	if err != nil {
		t.Fatal(err)
	}

	mock.MockWebsocketClientOnClientPool(p)
	// Rotate the server to make sure we can still send through the new client connect
	// to the pool chans
	p.RotateAndStartNextClient()

	go func() {
		p.CurrentClient().BlockChannel() <- model.Block{Hash: testHash}
	}()

	ticker := time.NewTicker(time.Second)
	select {
	case <-ticker.C:
		t.Error("Timed out waiting for block")
	case b := <-p.CurrentClient().BlockNotify():
		if b.Hash != testHash {
			t.Error("Returned incorrect block hash")
		}
	}
}

func TestClientPool_TransactionNotify(t *testing.T) {
	var (
		p, err     = client.NewClientPool([]string{"http://localhost:8332", "http://localhost:8336"}, nil)
		txChan     = p.TransactionNotify()
		expectedTx = factory.NewTransaction()
	)
	if err != nil {
		t.Fatal(err)
	}
	mock.MockWebsocketClientOnClientPool(p)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}

	// Rotate the server to make sure we can still send through the new
	// client connect to the pool chans
	p.RotateAndStartNextClient()

	go func() {
		p.CurrentClient().TxChannel() <- expectedTx
	}()

	ticker := time.NewTicker(1 * time.Second)
	select {
	case <-ticker.C:
		t.Error("Timed out waiting for tx")
	case b := <-txChan:
		for n, in := range b.Inputs {
			f, err := model.ToFloat(in.ValueIface)
			if err != nil {
				t.Error(err)
			}
			b.Inputs[n].Value = f
		}
		for n, out := range b.Outputs {
			f, err := model.ToFloat(out.ValueIface)
			if err != nil {
				t.Error(err)
			}
			b.Outputs[n].Value = f
		}
		test.ValidateTransaction(b, expectedTx, t)
	}
}

func TestClientPoolDoesntRaceWaitGroups(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	var p, err = client.NewClientPool([]string{"http://localhost:1111", "http://localhost:2222"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.MockWebsocketClientOnClientPool(p)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}

	httpmock.RegisterNoResponder(func(req *http.Request) (*http.Response, error) {
		time.Sleep(500 * time.Millisecond)
		return httpmock.NewJsonResponse(http.StatusOK, `{}`)
	})

	var wait sync.WaitGroup
	wait.Add(4)
	go func() {
		defer wait.Done()
		p.GetBestBlock()
	}()
	go func() {
		defer wait.Done()
		if err := p.RotateAndStartNextClient(); err != nil {
			t.Errorf("failed rotating client: %s", err)
		}
	}()
	go func() {
		defer wait.Done()
		p.GetBestBlock()
	}()
	go func() {
		defer wait.Done()
		if err := p.RotateAndStartNextClient(); err != nil {
			t.Errorf("failed rotating client: %s", err)
		}
	}()
	wait.Wait()
}
