package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// #############################################################################
// ##                           CONFIGURATION                                 ##
// #############################################################################

const (
	// -- Endpoints --
	RPC_URL             = "https://eth.llamarpc.com"
	FLASHBOTS_RELAY_URL = "https://relay.flashbots.net"

	// -- Contract Addresses (Mainnet) --
	UNISWAP_V2_ROUTER_ADDR = "0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D"
	WETH_ADDRESS           = "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"

	// -- Default Parameters --
	DEFAULT_ETH_AMOUNT       = "0.012"  // ETH to swap
	DEFAULT_TOKEN_ADDRESS    = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48" 
	DEFAULT_SLIPPAGE         = 0.01  // 0.1%
	DEFAULT_DEADLINE_SECONDS = 120    // 2 minutes
)

// Contract ABIs
const (
	routerABI = `[
		{
			"inputs": [
				{"internalType": "uint256", "name": "amountIn", "type": "uint256"},
				{"internalType": "address[]", "name": "path", "type": "address[]"}
			],
			"name": "getAmountsOut",
			"outputs": [{"internalType": "uint256[]", "name": "amounts", "type": "uint256[]"}],
			"stateMutability": "view",
			"type": "function"
		},
		{
			"inputs": [
				{"internalType": "uint256", "name": "amountOutMin", "type": "uint256"},
				{"internalType": "address[]", "name": "path", "type": "address[]"},
				{"internalType": "address", "name": "to", "type": "address"},
				{"internalType": "uint256", "name": "deadline", "type": "uint256"}
			],
			"name": "swapExactETHForTokens",
			"outputs": [{"internalType": "uint256[]", "name": "amounts", "type": "uint256[]"}],
			"stateMutability": "payable",
			"type": "function"
		},
		{
			"inputs": [
				{"internalType": "address", "name": "token", "type": "address"},
				{"internalType": "uint256", "name": "amountTokenDesired", "type": "uint256"},
				{"internalType": "uint256", "name": "amountTokenMin", "type": "uint256"},
				{"internalType": "uint256", "name": "amountETHMin", "type": "uint256"},
				{"internalType": "address", "name": "to", "type": "address"},
				{"internalType": "uint256", "name": "deadline", "type": "uint256"}
			],
			"name": "addLiquidityETH",
			"outputs": [
				{"internalType": "uint256", "name": "amountToken", "type": "uint256"},
				{"internalType": "uint256", "name": "amountETH", "type": "uint256"},
				{"internalType": "uint256", "name": "liquidity", "type": "uint256"}
			],
			"stateMutability": "payable",
			"type": "function"
		},
		{
			"inputs": [],
			"name": "factory",
			"outputs": [{"internalType": "address", "name": "", "type": "address"}],
			"stateMutability": "view",
			"type": "function"
		}
	]`

	factoryABI = `[
		{
			"inputs": [
				{"internalType": "address", "name": "tokenA", "type": "address"},
				{"internalType": "address", "name": "tokenB", "type": "address"}
			],
			"name": "getPair",
			"outputs": [{"internalType": "address", "name": "pair", "type": "address"}],
			"stateMutability": "view",
			"type": "function"
		}
	]`

	pairABI = `[
		{
			"inputs": [],
			"name": "getReserves",
			"outputs": [
				{"internalType": "uint112", "name": "_reserve0", "type": "uint112"},
				{"internalType": "uint112", "name": "_reserve1", "type": "uint112"},
				{"internalType": "uint32", "name": "_blockTimestampLast", "type": "uint32"}
			],
			"stateMutability": "view",
			"type": "function"
		},
		{
			"inputs": [],
			"name": "token0",
			"outputs": [{"internalType": "address", "name": "", "type": "address"}],
			"stateMutability": "view",
			"type": "function"
		}
	]`

	erc20ABI = `[
		{
			"inputs": [
				{"internalType": "address", "name": "spender", "type": "address"},
				{"internalType": "uint256", "name": "amount", "type": "uint256"}
			],
			"name": "approve",
			"outputs": [{"internalType": "bool", "name": "", "type": "bool"}],
			"stateMutability": "nonpayable",
			"type": "function"
		}
	]`
)

// #############################################################################
// ##                            STRUCTURES                                   ##
// #############################################################################

type Config struct {
	RpcURL             string
	EoaPrivateKey      string
	FlashbotsSignerKey string
	EthAmount          *big.Int
	TokenAddress       common.Address
	SlippageTolerance  float64
	DeadlineSeconds    int64
}

type FlashbotsBundle struct {
	Txs         []string `json:"txs"`
	BlockNumber string   `json:"blockNumber"`
}

type FlashbotsRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type FlashbotsSimulationResponse struct {
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

type FlashbotsSendResponse struct {
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

// #############################################################################
// ##                           MAIN EXECUTION                                ##
// #############################################################################

func main() {
	log.Println("🚀 Flashbots Atomic Uniswap V2 Operations")
	log.Println("==========================================")

	// Parse configuration
	config, err := parseConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Validate keys
	if config.EoaPrivateKey == "YOUR_EOA_PRIVATE_KEY" || 
	   config.FlashbotsSignerKey == "YOUR_FLASHBOTS_SIGNER_KEY" {
		log.Println("❌ Please set your actual private keys!")
		log.Println("Usage examples:")
		log.Println("  go run main.go --eoa-key=0x123... --flashbots-key=0x456...")
		log.Println("  Or set environment variables: EOA_PRIVATE_KEY and FLASHBOTS_SIGNER_KEY")
		return
	}

	ctx := context.Background()

	// Initialize Ethereum client
	client, err := ethclient.Dial(config.RpcURL)
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum: %v", err)
	}

	// Load private keys
	eoaKey, err := crypto.HexToECDSA(strings.TrimPrefix(config.EoaPrivateKey, "0x"))
	if err != nil {
		log.Fatalf("Invalid EOA private key: %v", err)
	}

	flashbotsKey, err := crypto.HexToECDSA(strings.TrimPrefix(config.FlashbotsSignerKey, "0x"))
	if err != nil {
		log.Fatalf("Invalid Flashbots signer key: %v", err)
	}

	eoaAddress := crypto.PubkeyToAddress(eoaKey.PublicKey)
	log.Printf("✅ EOA Address: %s", eoaAddress.Hex())

	// Get network parameters
	chainID, err := client.NetworkID(ctx)
	if err != nil {
		log.Fatalf("Failed to get chain ID: %v", err)
	}

	nonce, err := client.PendingNonceAt(ctx, eoaAddress)
	if err != nil {
		log.Fatalf("Failed to get nonce: %v", err)
	}

	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		log.Fatalf("Failed to get gas price: %v", err)
	}

	// Increase gas price by 20% for faster inclusion
	gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(120))
	gasPrice = new(big.Int).Div(gasPrice, big.NewInt(100))

	log.Printf("Chain ID: %s, Nonce: %d, Gas Price: %s Gwei", 
		chainID.String(), nonce, weiToGwei(gasPrice).Text('f', 2))

	// Display transaction plan
	ethFloat := new(big.Float).Quo(new(big.Float).SetInt(config.EthAmount), big.NewFloat(params.Ether))
	log.Printf("📋 Transaction Plan:")
	log.Printf("   • Swap %s ETH → %s", ethFloat.Text('f', 6), config.TokenAddress.Hex())
	log.Printf("   • Add liquidity with received tokens + remaining ETH")
	log.Printf("   • Slippage tolerance: %.2f%%", config.SlippageTolerance*100)

	// Execute atomic operations
	if err := executeAtomicOperations(ctx, client, config, eoaKey, flashbotsKey, chainID, nonce, gasPrice); err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	log.Println("🎉 Atomic operations completed successfully!")
}

// #############################################################################
// ##                        MAIN EXECUTION LOGIC                             ##
// #############################################################################

func executeAtomicOperations(ctx context.Context, client *ethclient.Client, config *Config, eoaKey, flashbotsKey *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasPrice *big.Int) error {
	eoaAddress := crypto.PubkeyToAddress(eoaKey.PublicKey)
	deadline := big.NewInt(time.Now().Unix() + config.DeadlineSeconds)

	// Parse router ABI
	routerContractABI, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		return fmt.Errorf("failed to parse router ABI: %v", err)
	}

	// Parse ERC20 ABI
	erc20ContractABI, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		return fmt.Errorf("failed to parse ERC20 ABI: %v", err)
	}

	// 1. Get expected token amount from swap
	log.Println("\n[1/5] Calculating expected token output...")
	path := []common.Address{common.HexToAddress(WETH_ADDRESS), config.TokenAddress}
	expectedTokenAmount, err := getAmountsOut(ctx, client, &routerContractABI, config.EthAmount, path)
	if err != nil {
		return fmt.Errorf("failed to get expected token amount: %v", err)
	}
	log.Printf("Expected token output: %s", formatTokenAmount(expectedTokenAmount, 18))

	log.Printf("Expected token output: %s", formatTokenAmount(expectedTokenAmount, 6)) 


	// 2. Create token approval transaction
	log.Println("\n[2/5] Creating token approval transaction...")
	approveTx, err := createApproveTransaction(eoaKey, chainID, nonce, gasPrice, config.TokenAddress, expectedTokenAmount, &erc20ContractABI)
	if err != nil {
		return fmt.Errorf("failed to create approve transaction: %v", err)
	}
	log.Printf("Approve TX hash: %s", approveTx.Hash().Hex())

	// 3. Create swap transaction
	log.Println("\n[3/5] Creating swap transaction...")
	amountOutMin := applySlippage(expectedTokenAmount, config.SlippageTolerance)
	swapTx, err := createSwapTransaction(ctx, client, eoaKey, chainID, eoaAddress, nonce+1, gasPrice, deadline, config.EthAmount, amountOutMin, path, &routerContractABI)
	if err != nil {
		return fmt.Errorf("failed to create swap transaction: %v", err)
	}
	log.Printf("Swap TX hash: %s", swapTx.Hash().Hex())

	// 4. Create add liquidity transaction
	log.Println("\n[4/5] Creating add liquidity transaction...")
	ethForLP, err := calculateOptimalETHForLP(ctx, client, config.TokenAddress, expectedTokenAmount)
	if err != nil {
		return fmt.Errorf("failed to calculate optimal ETH for LP: %v", err)
	}

	addLiquidityTx, err := createAddLiquidityTransaction(ctx, client, eoaKey, chainID, eoaAddress, nonce+2, gasPrice, deadline, config.TokenAddress, expectedTokenAmount, ethForLP, config.SlippageTolerance, &routerContractABI)
	if err != nil {
		return fmt.Errorf("failed to create add liquidity transaction: %v", err)
	}
	log.Printf("AddLiquidity TX hash: %s", addLiquidityTx.Hash().Hex())

	// 5. Bundle and send via Flashbots
	log.Println("\n[5/5] Bundling and sending to Flashbots...")
	transactions := []*types.Transaction{approveTx, swapTx, addLiquidityTx}

	// Simulate bundle first
	simResult, err := simulateBundle(ctx, transactions, flashbotsKey)
	if err != nil {
		log.Printf("⚠️  Bundle simulation failed: %v", err)
	} else {
		log.Println("✅ Bundle simulation successful!")
		for i, result := range simResult.Result.Results {
			if result.Error != "" {
				return fmt.Errorf("transaction %d simulation error: %s", i+1, result.Error)
			}
			log.Printf("   TX %d: Gas used %s, Gas fees %s ETH", i+1, result.GasUsed, weiToEth(result.GasFees))
		}
	}

	// Send bundle
	sendResult, err := sendBundle(ctx, transactions, flashbotsKey)
	if err != nil {
		return fmt.Errorf("failed to send bundle: %v", err)
	}

	log.Printf("🎯 Bundle submitted! Hash: %s", sendResult.Result.BundleHash)

	// Monitor for inclusion
	return monitorBundleInclusion(ctx, client, transactions, 30*time.Second)
}

// #############################################################################
// ##                        TRANSACTION CREATION                             ##
// #############################################################################

func createApproveTransaction(key *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasPrice *big.Int, tokenAddr common.Address, amount *big.Int, erc20ABI *abi.ABI) (*types.Transaction, error) {
	data, err := erc20ABI.Pack("approve", common.HexToAddress(UNISWAP_V2_ROUTER_ADDR), amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack approve data: %v", err)
	}

	tx := types.NewTransaction(nonce, tokenAddr, big.NewInt(0), 100000, gasPrice, data)
	return types.SignTx(tx, types.NewEIP155Signer(chainID), key)
}

func createSwapTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, chainID *big.Int, to common.Address, nonce uint64, gasPrice, deadline, value, amountOutMin *big.Int, path []common.Address, routerABI *abi.ABI) (*types.Transaction, error) {
	data, err := routerABI.Pack("swapExactETHForTokens", amountOutMin, path, to, deadline)
	if err != nil {
		return nil, fmt.Errorf("failed to pack swap data: %v", err)
	}

	// Estimate gas
	routerAddr := common.HexToAddress(UNISWAP_V2_ROUTER_ADDR)
	// gasLimit, err := client.EstimateGas(ctx, ethereum.CallMsg{
	// 	From:  to,
	// 	To:    &routerAddr,
	// 	Value: value,
	// 	Data:  data,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to estimate gas: %v", err)
	// }
	gasLimit := uint64(334_000_000)

	// Add 20% buffer
	gasLimit = gasLimit * 120 / 100

	tx := types.NewTransaction(nonce, routerAddr, value, gasLimit, gasPrice, data)
	return types.SignTx(tx, types.NewEIP155Signer(chainID), key)
}

func createAddLiquidityTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, chainID *big.Int, to common.Address, nonce uint64, gasPrice, deadline *big.Int, tokenAddr common.Address, tokenAmount, ethAmount *big.Int, slippage float64, routerABI *abi.ABI) (*types.Transaction, error) {
	amountTokenMin := applySlippage(tokenAmount, slippage)
	amountETHMin := applySlippage(ethAmount, slippage)

	data, err := routerABI.Pack("addLiquidityETH", tokenAddr, tokenAmount, amountTokenMin, amountETHMin, to, deadline)
	if err != nil {
		return nil, fmt.Errorf("failed to pack add liquidity data: %v", err)
	}

	// Estimate gas
	routerAddr := common.HexToAddress(UNISWAP_V2_ROUTER_ADDR)
	// gasLimit, err := client.EstimateGas(ctx, ethereum.CallMsg{
	// 	From:  to,
	// 	To:    &routerAddr,
	// 	Value: ethAmount,
	// 	Data:  data,
	// })
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to estimate gas: %v", err)
	// }
	gasLimit := uint64(334_000_000) // Set a fixed gas limit for addLiquidity

	// Add 20% buffer
	gasLimit = gasLimit * 120 / 100

	tx := types.NewTransaction(nonce, routerAddr, ethAmount, gasLimit, gasPrice, data)
	return types.SignTx(tx, types.NewEIP155Signer(chainID), key)
}

// #############################################################################
// ##                          FLASHBOTS INTEGRATION                          ##
// #############################################################################

func simulateBundle(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey) (*FlashbotsSimulationResponse, error) {
	// Encode transactions
	var txsHex []string
	for _, tx := range txs {
		rawTx, err := rlp.EncodeToBytes(tx)
		if err != nil {
			return nil, fmt.Errorf("failed to encode transaction: %v", err)
		}
		txsHex = append(txsHex, hexutil.Encode(rawTx))
	}

	// Get target block
	client, _ := ethclient.Dial(RPC_URL)
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block: %v", err)
	}
	targetBlock := header.Number.Uint64() + 1

	// Prepare simulation request
	params := map[string]interface{}{
		"txs":              txsHex,
		"blockNumber":      fmt.Sprintf("0x%x", targetBlock),
		"stateBlockNumber": "latest",
	}

	request := FlashbotsRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "eth_callBundle",
		Params:  []interface{}{params},
	}

	return sendFlashbotsRequest[FlashbotsSimulationResponse](ctx, request, authKey)
}

func sendBundle(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey) (*FlashbotsSendResponse, error) {
	// Encode transactions
	var txsHex []string
	for _, tx := range txs {
		rawTx, err := rlp.EncodeToBytes(tx)
		if err != nil {
			return nil, fmt.Errorf("failed to encode transaction: %v", err)
		}
		txsHex = append(txsHex, hexutil.Encode(rawTx))
	}

	// Get target block
	client, _ := ethclient.Dial(RPC_URL)
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block: %v", err)
	}
	targetBlock := header.Number.Uint64() + 1

	// Prepare send request
	params := FlashbotsBundle{
		Txs:         txsHex,
		BlockNumber: fmt.Sprintf("0x%x", targetBlock),
	}

	request := FlashbotsRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "eth_sendBundle",
		Params:  []interface{}{params},
	}

	return sendFlashbotsRequest[FlashbotsSendResponse](ctx, request, authKey)
}

func sendFlashbotsRequest[T any](ctx context.Context, request FlashbotsRequest, authKey *ecdsa.PrivateKey) (*T, error) {
	// Marshal request
	reqBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", FLASHBOTS_RELAY_URL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	// Sign request
	signature, err := signFlashbotsPayload(reqBody, authKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign request: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Flashbots-Signature", signature)

	// Send request
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// Read and parse response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var result T
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	return &result, nil
}

func signFlashbotsPayload(body []byte, key *ecdsa.PrivateKey) (string, error) {
	hash := crypto.Keccak256Hash(body)
	signature, err := crypto.Sign(hash.Bytes(), key)
	if err != nil {
		return "", fmt.Errorf("failed to sign: %v", err)
	}

	address := crypto.PubkeyToAddress(key.PublicKey)
	return fmt.Sprintf("%s:0x%s", address.Hex(), hex.EncodeToString(signature)), nil
}

// #############################################################################
// ##                            UNISWAP HELPERS                              ##
// #############################################################################

func getAmountsOut(ctx context.Context, client *ethclient.Client, routerABI *abi.ABI, amountIn *big.Int, path []common.Address) (*big.Int, error) {
	data, err := routerABI.Pack("getAmountsOut", amountIn, path)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getAmountsOut: %v", err)
	}

	routerAddr := common.HexToAddress(UNISWAP_V2_ROUTER_ADDR)
	result, err := client.CallContract(ctx, ethereum.CallMsg{
		To:   &routerAddr,
		Data: data,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call getAmountsOut: %v", err)
	}

	var amounts []*big.Int
	err = routerABI.UnpackIntoInterface(&amounts, "getAmountsOut", result)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack amounts: %v", err)
	}

	if len(amounts) < 2 {
		return nil, fmt.Errorf("invalid amounts returned")
	}

	return amounts[1], nil
}

func calculateOptimalETHForLP(ctx context.Context, client *ethclient.Client, tokenAddr common.Address, tokenAmount *big.Int) (*big.Int, error) {
	// Parse ABIs
	routerContractABI, _ := abi.JSON(strings.NewReader(routerABI))
	factoryContractABI, _ := abi.JSON(strings.NewReader(factoryABI))
	pairContractABI, _ := abi.JSON(strings.NewReader(pairABI))

	routerAddr := common.HexToAddress(UNISWAP_V2_ROUTER_ADDR)
	wethAddr := common.HexToAddress(WETH_ADDRESS)

	// Get factory address
	factoryData, _ := routerContractABI.Pack("factory")
	factoryResult, err := client.CallContract(ctx, ethereum.CallMsg{To: &routerAddr, Data: factoryData}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get factory address: %v", err)
	}
	var factoryAddr common.Address
	routerContractABI.UnpackIntoInterface(&factoryAddr, "factory", factoryResult)

	// Get pair address
	pairData, _ := factoryContractABI.Pack("getPair", tokenAddr, wethAddr)
	pairResult, err := client.CallContract(ctx, ethereum.CallMsg{To: &factoryAddr, Data: pairData}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get pair address: %v", err)
	}
	var pairAddr common.Address
	factoryContractABI.UnpackIntoInterface(&pairAddr, "getPair", pairResult)

	if pairAddr == (common.Address{}) {
		return nil, fmt.Errorf("pair does not exist")
	}

	// Get token0 address
	token0Data, _ := pairContractABI.Pack("token0")
	token0Result, err := client.CallContract(ctx, ethereum.CallMsg{To: &pairAddr, Data: token0Data}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get token0: %v", err)
	}
	var token0 common.Address
	pairContractABI.UnpackIntoInterface(&token0, "token0", token0Result)

	// Get reserves
	reservesData, _ := pairContractABI.Pack("getReserves")
	reservesResult, err := client.CallContract(ctx, ethereum.CallMsg{To: &pairAddr, Data: reservesData}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get reserves: %v", err)
	}

	var reserves struct {
		Reserve0           *big.Int
		Reserve1           *big.Int
		BlockTimestampLast uint32
	}
	pairContractABI.UnpackIntoInterface(&reserves, "getReserves", reservesResult)

	// Determine which reserve corresponds to which token
	var ethReserve, tokenReserve *big.Int
	if token0 == wethAddr {
		ethReserve = reserves.Reserve0
		tokenReserve = reserves.Reserve1
	} else {
		ethReserve = reserves.Reserve1
		tokenReserve = reserves.Reserve0
	}

	// Calculate optimal ETH amount based on current ratio
	// Formula: ethForLP = (tokenAmount * ethReserve) / tokenReserve
	ethForLP := new(big.Int).Mul(tokenAmount, ethReserve)
	ethForLP = new(big.Int).Div(ethForLP, tokenReserve)

	// Use 80% of calculated amount to account for price impact
	ethForLP = new(big.Int).Mul(ethForLP, big.NewInt(80))
	ethForLP = new(big.Int).Div(ethForLP, big.NewInt(100))

	return ethForLP, nil
}

// #############################################################################
// ##                          UTILITY FUNCTIONS                              ##
// #############################################################################

func parseConfig() (*Config, error) {
	config := &Config{
		RpcURL:             RPC_URL,
		EoaPrivateKey:      getEnvOrDefault("EOA_PRIVATE_KEY", "YOUR_EOA_PRIVATE_KEY"),
		FlashbotsSignerKey: getEnvOrDefault("FLASHBOTS_SIGNER_KEY", "YOUR_FLASHBOTS_SIGNER_KEY"),
		TokenAddress:       common.HexToAddress(getEnvOrDefault("TOKEN_ADDRESS", DEFAULT_TOKEN_ADDRESS)),
		SlippageTolerance:  DEFAULT_SLIPPAGE,
		DeadlineSeconds:    DEFAULT_DEADLINE_SECONDS,
	}

	// Parse ETH amount
	ethAmountStr := getEnvOrDefault("ETH_AMOUNT", DEFAULT_ETH_AMOUNT)
	ethAmount, err := parseEtherAmount(ethAmountStr)
	if err != nil {
		return nil, fmt.Errorf("invalid ETH amount: %v", err)
	}
	config.EthAmount = ethAmount

	// Parse slippage if provided
	if slippageStr := os.Getenv("SLIPPAGE"); slippageStr != "" {
		slippage, err := strconv.ParseFloat(slippageStr, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid slippage: %v", err)
		}
		config.SlippageTolerance = slippage
	}

	// Parse deadline if provided
	if deadlineStr := os.Getenv("DEADLINE_SECONDS"); deadlineStr != "" {
		deadline, err := strconv.ParseInt(deadlineStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid deadline: %v", err)
		}
		config.DeadlineSeconds = deadline
	}

	// Parse command line arguments
	for i, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--eoa-key=") {
			config.EoaPrivateKey = strings.TrimPrefix(arg, "--eoa-key=")
		} else if strings.HasPrefix(arg, "--flashbots-key=") {
			config.FlashbotsSignerKey = strings.TrimPrefix(arg, "--flashbots-key=")
		} else if strings.HasPrefix(arg, "--token=") {
			config.TokenAddress = common.HexToAddress(strings.TrimPrefix(arg, "--token="))
		} else if strings.HasPrefix(arg, "--eth-amount=") {
			ethAmountStr := strings.TrimPrefix(arg, "--eth-amount=")
			ethAmount, err := parseEtherAmount(ethAmountStr)
			if err != nil {
				return nil, fmt.Errorf("invalid ETH amount in arg %d: %v", i+1, err)
			}
			config.EthAmount = ethAmount
		}
	}

	return config, nil
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func parseEtherAmount(s string) (*big.Int, error) {
	ethFloat, ok := new(big.Float).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid number format: %s", s)
	}

	weiFloat := new(big.Float).Mul(ethFloat, big.NewFloat(params.Ether))
	wei, _ := weiFloat.Int(nil)
	return wei, nil
}

func applySlippage(amount *big.Int, slippagePercent float64) *big.Int {
	slippageMultiplier := big.NewFloat(1.0 - slippagePercent)
	amountFloat := new(big.Float).SetInt(amount)
	minAmountFloat := new(big.Float).Mul(amountFloat, slippageMultiplier)
	minAmount, _ := minAmountFloat.Int(nil)
	return minAmount
}

func weiToGwei(wei *big.Int) *big.Float {
	return new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(params.GWei))
}

func weiToEth(weiStr string) string {
	wei, ok := new(big.Int).SetString(weiStr, 10)
	if !ok {
		return "0"
	}
	ethFloat := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(params.Ether))
	return ethFloat.Text('f', 6)
}

func formatTokenAmount(amount *big.Int, decimals int) string {
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	tokenFloat := new(big.Float).Quo(new(big.Float).SetInt(amount), new(big.Float).SetInt(divisor))
	return tokenFloat.Text('f', 6)
}

func monitorBundleInclusion(ctx context.Context, client *ethclient.Client, txs []*types.Transaction, timeout time.Duration) error {
	log.Printf("⏳ Monitoring bundle inclusion (timeout: %v)...", timeout)
	
	startTime := time.Now()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	txHashes := make([]common.Hash, len(txs))
	for i, tx := range txs {
		txHashes[i] = tx.Hash()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout):
			return fmt.Errorf("bundle inclusion timeout after %v", timeout)
		case <-ticker.C:
			// Check if any transaction is included
			for i, txHash := range txHashes {
				receipt, err := client.TransactionReceipt(ctx, txHash)
				if err == nil && receipt != nil {
					log.Printf("✅ Transaction %d included in block %d", i+1, receipt.BlockNumber.Uint64())
					if i == len(txHashes)-1 {
						log.Printf("🎉 All transactions included! Total time: %v", time.Since(startTime))
						return nil
					}
				}
			}

			// Log progress
			elapsed := time.Since(startTime)
			log.Printf("⏱️  Still monitoring... elapsed: %v", elapsed.Truncate(time.Second))
		}
	}
}

func init() {
	// Set up logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
