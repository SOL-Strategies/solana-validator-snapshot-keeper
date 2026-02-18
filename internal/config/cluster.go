package config

import (
	"fmt"

	"github.com/sol-strategies/solana-validator-snapshot-keeper/internal/constants"
)

type Cluster struct {
	Name   string `koanf:"name"`
	RPCURL string `koanf:"rpc_url"`
}

func (c *Cluster) Validate() error {
	if !constants.IsValidCluster(c.Name) {
		return fmt.Errorf("invalid cluster name %q, must be one of: %v", c.Name, constants.ValidClusters)
	}
	return nil
}

func (c *Cluster) EffectiveRPCURL() string {
	if c.RPCURL != "" {
		return c.RPCURL
	}
	if url, ok := constants.ClusterRPCURLs[c.Name]; ok {
		return url
	}
	return ""
}
