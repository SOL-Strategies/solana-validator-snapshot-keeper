package constants

const (
	ClusterMainnetBeta = "mainnet-beta"
	ClusterTestnet     = "testnet"
)

var (
	ValidClusters = []string{ClusterMainnetBeta, ClusterTestnet}

	ClusterRPCURLs = map[string]string{
		ClusterMainnetBeta: "https://api.mainnet-beta.solana.com",
		ClusterTestnet:     "https://api.testnet.solana.com",
	}
)

func IsValidCluster(name string) bool {
	for _, c := range ValidClusters {
		if c == name {
			return true
		}
	}
	return false
}
