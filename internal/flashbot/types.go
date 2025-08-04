package flashbot

type Bundle struct {
	Txs         []string `json:"txs"`
	BlockNumber string   `json:"blockNumber"`
}

type Request struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type SimulationResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		BundleGasPrice    string `json:"bundleGasPrice"`
		BundleHash        string `json:"bundleHash"`
		CoinbaseDiff      string `json:"coinbaseDiff"`
		EthSentToCoinbase string `json:"ethSentToCoinbase"`
		GasFees           string `json:"gasFees"`
		Results           []struct {
			CoinbaseDiff      string `json:"coinbaseDiff"`
			EthSentToCoinbase string `json:"ethSentToCoinbase"`
			FromAddress       string `json:"fromAddress"`
			GasFees           string `json:"gasFees"`
			GasPrice          string `json:"gasPrice"`
			GasUsed           string `json:"gasUsed"`
			ToAddress         string `json:"toAddress"`
			TxHash            string `json:"txHash"`
			Value             string `json:"value"`
			Error             string `json:"error,omitempty"`
			Revert            string `json:"revert,omitempty"`
		} `json:"results"`
		StateBlockNumber int64 `json:"stateBlockNumber"`
		TotalGasUsed     int64 `json:"totalGasUsed"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type SendResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		BundleHash string `json:"bundleHash"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}