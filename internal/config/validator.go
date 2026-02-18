package config

import "fmt"

type Validator struct {
	RPCURL              string `koanf:"rpc_url"`
	ActiveIdentityPubkey string `koanf:"active_identity_pubkey"`
}

func (v *Validator) Validate() error {
	if v.RPCURL == "" {
		return fmt.Errorf("validator.rpc_url is required")
	}
	if v.ActiveIdentityPubkey == "" {
		return fmt.Errorf("validator.active_identity_pubkey is required")
	}
	return nil
}
