package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/charmbracelet/log"
)

func logger() *log.Logger { return log.Default().WithPrefix("rpc") }

type Client struct {
	url        string
	httpClient *http.Client
}

func NewClient(url string) *Client {
	return &Client{
		url: url,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) call(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshalling response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// GetIdentity returns the current identity pubkey of the validator.
func (c *Client) GetIdentity(ctx context.Context) (string, error) {
	result, err := c.call(ctx, "getIdentity", nil)
	if err != nil {
		return "", fmt.Errorf("getIdentity: %w", err)
	}

	var identity struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(result, &identity); err != nil {
		return "", fmt.Errorf("parsing getIdentity result: %w", err)
	}

	logger().Debug("got identity", "pubkey", identity.Identity)
	return identity.Identity, nil
}

// GetSlot returns the current slot number.
func (c *Client) GetSlot(ctx context.Context) (uint64, error) {
	result, err := c.call(ctx, "getSlot", nil)
	if err != nil {
		return 0, fmt.Errorf("getSlot: %w", err)
	}

	var slot uint64
	if err := json.Unmarshal(result, &slot); err != nil {
		return 0, fmt.Errorf("parsing getSlot result: %w", err)
	}

	logger().Debug("got slot", "slot", slot)
	return slot, nil
}

// ClusterNode represents a node in the cluster as returned by getClusterNodes.
type ClusterNode struct {
	Pubkey  string  `json:"pubkey"`
	Gossip  string  `json:"gossip"`
	RPC     *string `json:"rpc"`
	Version *string `json:"version"`
}

// GetClusterNodes returns all nodes in the cluster.
func (c *Client) GetClusterNodes(ctx context.Context) ([]ClusterNode, error) {
	result, err := c.call(ctx, "getClusterNodes", nil)
	if err != nil {
		return nil, fmt.Errorf("getClusterNodes: %w", err)
	}

	var nodes []ClusterNode
	if err := json.Unmarshal(result, &nodes); err != nil {
		return nil, fmt.Errorf("parsing getClusterNodes result: %w", err)
	}

	logger().Debug("got cluster nodes", "count", len(nodes))
	return nodes, nil
}
