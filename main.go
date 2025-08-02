package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts"
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
	DEFAULT_ETH_AMOUNT       = "0.012" // ETH to swap
	DEFAULT_TOKEN_ADDRESS    = "0xF7285d17dded63A4480A0f1F0a8cc706F02dDa0a"
	DEFAULT_SLIPPAGE         = 0.01 // 1%
	DEFAULT_DEADLINE_SECONDS = 120  // 2 minutes

	// -- Dynamic Gas Parameters --
	PRIORITY_FEE_MULTIPLIER  = 3.0  // 3x current priority fee for fast inclusion
	BASE_FEE_MULTIPLIER      = 2.5  // 2.5x current base fee buffer
	GAS_LIMIT_BUFFER_PERCENT = 30   // 30% buffer on gas estimates
	MIN_PRIORITY_FEE_GWEI    = 2.0  // Minimum 2 Gwei priority fee
	MAX_PRIORITY_FEE_GWEI    = 50.0 // Maximum 50 Gwei priority fee
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

type GasParams struct {
	GasLimit       uint64
	MaxFeePerGas   *big.Int
	MaxPriorityFee *big.Int
	IsLegacy       bool
	LegacyGasPrice *big.Int
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
	log.Println("🚀 Flashbots Atomic Uniswap V2 Operations (Dynamic Gas)")
	log.Println("====================================================")

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

	// Calculate dynamic gas parameters
	gasParams, err := calculateDynamicGasParams(ctx, client)
	if err != nil {
		log.Fatalf("Failed to calculate gas parameters: %v", err)
	}

	if gasParams.IsLegacy {
		log.Printf("⛽ Using legacy gas: %s Gwei", weiToGwei(gasParams.LegacyGasPrice).Text('f', 2))
	} else {
		log.Printf("⛽ Using EIP-1559: MaxFee=%s Gwei, PriorityFee=%s Gwei",
			weiToGwei(gasParams.MaxFeePerGas).Text('f', 2),
			weiToGwei(gasParams.MaxPriorityFee).Text('f', 2))
	}

	log.Printf("Chain ID: %s, Nonce: %d", chainID.String(), nonce)

	// Display transaction plan
	ethFloat := new(big.Float).Quo(new(big.Float).SetInt(config.EthAmount), big.NewFloat(params.Ether))
	log.Printf("📋 Transaction Plan:")
	log.Printf("   • Swap %s ETH → %s", ethFloat.Text('f', 6), config.TokenAddress.Hex())
	log.Printf("   • Add liquidity with received tokens + remaining ETH")
	log.Printf("   • Slippage tolerance: %.2f%%", config.SlippageTolerance*100)

	// Execute atomic operations
	if err := executeAtomicOperations(ctx, client, config, eoaKey, flashbotsKey, chainID, nonce, gasParams); err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	log.Println("🎉 Atomic operations completed successfully!")
}

// #############################################################################
// ##                        MAIN EXECUTION LOGIC                             ##
// #############################################################################

// func executeAtomicOperations(ctx context.Context, client *ethclient.Client, config *Config, eoaKey, flashbotsKey *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasParams *GasParams) error {
// 	eoaAddress := crypto.PubkeyToAddress(eoaKey.PublicKey)
// 	deadline := big.NewInt(time.Now().Unix() + config.DeadlineSeconds)

// 	// Parse ABIs
// 	routerContractABI, err := abi.JSON(strings.NewReader(routerABI))
// 	if err != nil {
// 		return fmt.Errorf("failed to parse router ABI: %v", err)
// 	}

// 	erc20ContractABI, err := abi.JSON(strings.NewReader(erc20ABI))
// 	if err != nil {
// 		return fmt.Errorf("failed to parse ERC20 ABI: %v", err)
// 	}

// 	// 1. Get expected token amount from swap
// 	log.Println("\n[1/5] Calculating expected token output...")
// 	path := []common.Address{common.HexToAddress(WETH_ADDRESS), config.TokenAddress}
// 	expectedTokenAmount, err := getAmountsOut(ctx, client, &routerContractABI, config.EthAmount, path)
// 	if err != nil {
// 		return fmt.Errorf("failed to get expected token amount: %v", err)
// 	}
// 	log.Printf("Expected token output: %s", formatTokenAmount(expectedTokenAmount, 6))

// 	// 2. Create token approval transaction
// 	log.Println("\n[2/5] Creating token approval transaction...")
// 	approveTx, err := createApproveTransaction(ctx, client, eoaKey, chainID, nonce, gasParams, config.TokenAddress, expectedTokenAmount, &erc20ContractABI)
// 	if err != nil {
// 		return fmt.Errorf("failed to create approve transaction: %v", err)
// 	}
// 	log.Printf("Approve TX hash: %s (Gas: %d)", approveTx.Hash().Hex(), approveTx.Gas())

// 	// 3. Create swap transaction
// 	log.Println("\n[3/5] Creating swap transaction...")
// 	amountOutMin := applySlippage(expectedTokenAmount, config.SlippageTolerance)
// 	swapTx, err := createSwapTransaction(ctx, client, eoaKey, chainID, eoaAddress, nonce+1, gasParams, deadline, config.EthAmount, amountOutMin, path, &routerContractABI)
// 	if err != nil {
// 		return fmt.Errorf("failed to create swap transaction: %v", err)
// 	}
// 	log.Printf("Swap TX hash: %s (Gas: %d)", swapTx.Hash().Hex(), swapTx.Gas())

// 	// 4. Create add liquidity transaction
// 	log.Println("\n[4/5] Creating add liquidity transaction...")
// 	ethForLP, err := calculateOptimalETHForLP(ctx, client, config.TokenAddress, expectedTokenAmount)
// 	if err != nil {
// 		return fmt.Errorf("failed to calculate optimal ETH for LP: %v", err)
// 	}

// 	addLiquidityTx, err := createAddLiquidityTransaction(ctx, client, eoaKey, chainID, eoaAddress, nonce+2, gasParams, deadline, config.TokenAddress, expectedTokenAmount, ethForLP, config.SlippageTolerance, &routerContractABI)
// 	if err != nil {
// 		return fmt.Errorf("failed to create add liquidity transaction: %v", err)
// 	}
// 	log.Printf("AddLiquidity TX hash: %s (Gas: %d)", addLiquidityTx.Hash().Hex(), addLiquidityTx.Gas())

// 	// 5. Bundle and send via Flashbots
// 	log.Println("\n[5/5] Bundling and sending to Flashbots...")
// 	transactions := []*types.Transaction{approveTx, swapTx, addLiquidityTx}

// 	// Calculate total gas fees
// 	totalGasUsed := approveTx.Gas() + swapTx.Gas() + addLiquidityTx.Gas()
// 	var totalFees *big.Int
// 	if gasParams.IsLegacy {
// 		totalFees = new(big.Int).Mul(gasParams.LegacyGasPrice, new(big.Int).SetUint64(totalGasUsed))
// 	} else {
// 		totalFees = new(big.Int).Mul(gasParams.MaxFeePerGas, new(big.Int).SetUint64(totalGasUsed))
// 	}
// 	log.Printf("📊 Bundle Stats: Total Gas=%d, Est. Fees=~%s ETH", totalGasUsed, weiToEth(totalFees.String()))

// 	// Simulate bundle first
// 	simResult, err := simulateBundle(ctx, transactions, flashbotsKey)
// 	if err != nil {
// 		log.Printf("⚠️  Bundle simulation failed: %v", err)
// 	} else {
// 		log.Println("✅ Bundle simulation successful!")
// 		for i, result := range simResult.Result.Results {
// 			if result.Error != "" {
// 				return fmt.Errorf("transaction %d simulation error: %s", i+1, result.Error)
// 			}
// 			log.Printf("   TX %d: Gas used %s, Gas fees %s ETH", i+1, result.GasUsed, weiToEth(result.GasFees))
// 		}
// 	}

// 	// Send bundle with retries for better inclusion chance
// 	sendResult, err := sendBundleWithRetries(ctx, transactions, flashbotsKey, 3)
// 	if err != nil {
// 		return fmt.Errorf("failed to send bundle: %v", err)
// 	}

// 	log.Printf("🎯 Bundle submitted! Hash: %s", sendResult.Result.BundleHash)

// 	// Monitor for inclusion with faster polling
// 	return monitorBundleInclusion(ctx, client, transactions, 60*time.Second)
// }

func executeAtomicOperations(ctx context.Context, client *ethclient.Client, config *Config, eoaKey, flashbotsKey *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasParams *GasParams) error {
	eoaAddress := crypto.PubkeyToAddress(eoaKey.PublicKey)
	deadline := big.NewInt(time.Now().Unix() + config.DeadlineSeconds)

	// Parse ABIs
	routerContractABI, err := abi.JSON(strings.NewReader(routerABI))
	if err != nil {
		return fmt.Errorf("failed to parse router ABI: %v", err)
	}

	erc20ContractABI, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		return fmt.Errorf("failed to parse ERC20 ABI: %v", err)
	}

	// ✅ Split initial ETH: 50% for swap, 50% for liquidity
	two := big.NewInt(2)
	ethForSwap := new(big.Int).Div(config.EthAmount, two)
	// Use the remaining ETH for LP to avoid dust from division
	ethForLP := new(big.Int).Sub(config.EthAmount, ethForSwap)

	// 1. Calculate token output from swapping HALF the ETH
	log.Println("\n[1/5] Calculating expected token output...")
	path := []common.Address{common.HexToAddress(WETH_ADDRESS), config.TokenAddress}
	expectedTokenAmount, err := getAmountsOut(ctx, client, &routerContractABI, ethForSwap, path)
	if err != nil {
		return fmt.Errorf("failed to get expected token amount: %v", err)
	}
	log.Printf("Expected token output: %s", formatTokenAmount(expectedTokenAmount, 6))

	// 2. Create token approval transaction
	log.Println("\n[2/5] Creating token approval transaction...")
	approveTx, err := createApproveTransaction(ctx, client, eoaKey, chainID, nonce, gasParams, config.TokenAddress, expectedTokenAmount, &erc20ContractABI)
	if err != nil {
		return fmt.Errorf("failed to create approve transaction: %v", err)
	}
	log.Printf("Approve TX hash: %s (Gas: %d)", approveTx.Hash().Hex(), approveTx.Gas())

	// 3. Create swap transaction with ethForSwap
	log.Println("\n[3/5] Creating swap transaction...")
	amountOutMin := applySlippage(expectedTokenAmount, config.SlippageTolerance)
	swapTx, err := createSwapTransaction(ctx, client, eoaKey, chainID, eoaAddress, nonce+1, gasParams, deadline, ethForSwap, amountOutMin, path, &routerContractABI)
	if err != nil {
		return fmt.Errorf("failed to create swap transaction: %v", err)
	}
	log.Printf("Swap TX hash: %s (Gas: %d)", swapTx.Hash().Hex(), swapTx.Gas())

	// 4. Create add liquidity transaction with ethForLP
	log.Println("\n[4/5] Creating add liquidity transaction...")
	addLiquidityTx, err := createAddLiquidityTransaction(ctx, client, eoaKey, chainID, eoaAddress, nonce+2, gasParams, deadline, config.TokenAddress, expectedTokenAmount, ethForLP, config.SlippageTolerance, &routerContractABI)
	if err != nil {
		return fmt.Errorf("failed to create add liquidity transaction: %v", err)
	}
	log.Printf("AddLiquidity TX hash: %s (Gas: %d)", addLiquidityTx.Hash().Hex(), addLiquidityTx.Gas())

	// 5. Bundle and send via Flashbots
	log.Println("\n[5/5] Bundling and sending to Flashbots...")
	transactions := []*types.Transaction{approveTx, swapTx, addLiquidityTx}

	// Calculate total gas fees
	totalGasUsed := approveTx.Gas() + swapTx.Gas() + addLiquidityTx.Gas()
	var totalFees *big.Int
	if gasParams.IsLegacy {
		totalFees = new(big.Int).Mul(gasParams.LegacyGasPrice, new(big.Int).SetUint64(totalGasUsed))
	} else {
		totalFees = new(big.Int).Mul(gasParams.MaxFeePerGas, new(big.Int).SetUint64(totalGasUsed))
	}
	log.Printf("📊 Bundle Stats: Total Gas=%d, Est. Fees=~%s ETH", totalGasUsed, weiToEth(totalFees.String()))

	// Simulate bundle first
	simResult, err := simulateBundle(ctx, transactions, flashbotsKey)
	if err != nil {
		log.Printf("⚠️  Bundle simulation failed: %v", err)
	} else if simResult.Error != nil {
		return fmt.Errorf("bundle simulation returned an error: %s", simResult.Error.Message)
	} else {
		log.Println("✅ Bundle simulation successful!")
		for i, result := range simResult.Result.Results {
			if result.Error != "" {
				return fmt.Errorf("transaction %d simulation error: %s - %s", i+1, result.Error, result.Revert)
			}
			log.Printf("   TX %d: Gas used %s, Gas fees %s ETH", i+1, result.GasUsed, weiToEth(result.GasFees))
		}
	}

	// Send bundle with retries for better inclusion chance
	sendResult, err := sendBundleWithRetries(ctx, transactions, flashbotsKey, 3)
	if err != nil {
		return fmt.Errorf("failed to send bundle: %v", err)
	}

	log.Printf("🎯 Bundle submitted! Hash: %s", sendResult.Result.BundleHash)

	// Monitor for inclusion with faster polling
	return monitorBundleInclusion(ctx, client, transactions, 60*time.Second)
}

// #############################################################################
// ##                        DYNAMIC GAS CALCULATION                          ##
// #############################################################################

func calculateDynamicGasParams(ctx context.Context, client *ethclient.Client) (*GasParams, error) {
	// Get latest block header
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block header: %v", err)
	}

	// Check if EIP-1559 is active
	if header.BaseFee == nil {
		// Legacy gas pricing
		gasPrice, err := client.SuggestGasPrice(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get legacy gas price: %v", err)
		}

		// Increase by 50% for faster inclusion
		fastGasPrice := new(big.Int).Mul(gasPrice, big.NewInt(150))
		fastGasPrice = new(big.Int).Div(fastGasPrice, big.NewInt(100))

		return &GasParams{
			IsLegacy:       true,
			LegacyGasPrice: fastGasPrice,
		}, nil
	}

	// EIP-1559 dynamic gas calculation
	baseFee := header.BaseFee

	// Get current priority fee suggestion
	priorityFee, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		// Fallback to minimum priority fee
		priorityFee = gweiToWei(MIN_PRIORITY_FEE_GWEI)
	}

	// Apply multiplier for faster inclusion
	priorityFee = new(big.Int).Mul(priorityFee, big.NewInt(int64(PRIORITY_FEE_MULTIPLIER*100)))
	priorityFee = new(big.Int).Div(priorityFee, big.NewInt(100))

	// Enforce min/max bounds
	minPriorityFee := gweiToWei(MIN_PRIORITY_FEE_GWEI)
	maxPriorityFee := gweiToWei(MAX_PRIORITY_FEE_GWEI)

	if priorityFee.Cmp(minPriorityFee) < 0 {
		priorityFee = minPriorityFee
	}
	if priorityFee.Cmp(maxPriorityFee) > 0 {
		priorityFee = maxPriorityFee
	}

	// Calculate maxFeePerGas = (baseFee * multiplier) + priorityFee
	maxBaseFee := new(big.Float).Mul(new(big.Float).SetInt(baseFee), big.NewFloat(BASE_FEE_MULTIPLIER))
	maxBaseFeeInt, _ := maxBaseFee.Int(nil)
	maxFeePerGas := new(big.Int).Add(maxBaseFeeInt, priorityFee)

	log.Printf("🔥 Gas Market Analysis:")
	log.Printf("   • Current Base Fee: %s Gwei", weiToGwei(baseFee).Text('f', 2))
	log.Printf("   • Dynamic Priority Fee: %s Gwei", weiToGwei(priorityFee).Text('f', 2))
	log.Printf("   • Max Fee Per Gas: %s Gwei", weiToGwei(maxFeePerGas).Text('f', 2))

	return &GasParams{
		MaxFeePerGas:   maxFeePerGas,
		MaxPriorityFee: priorityFee,
		IsLegacy:       false,
	}, nil
}

func estimateGasWithRetry(ctx context.Context, client *ethclient.Client, msg ethereum.CallMsg, retries int) (uint64, error) {
	var lastErr error

	for i := 0; i < retries; i++ {
		gasLimit, err := client.EstimateGas(ctx, msg)
		if err == nil {
			// Add buffer to prevent out-of-gas errors
			bufferedGas := gasLimit * (100 + GAS_LIMIT_BUFFER_PERCENT) / 100
			return bufferedGas, nil
		}

		lastErr = err
		if i < retries-1 {
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}

	return 0, fmt.Errorf("gas estimation failed after %d retries: %v", retries, lastErr)
}

func getDefaultGasLimits(operation string) uint64 {
	switch operation {
	case "approve":
		return 60000
	case "swap":
		return 300000
	case "addLiquidity":
		return 400000
	default:
		return 200000
	}
}

// #############################################################################
// ##                        TRANSACTION CREATION                             ##
// #############################################################################

func createApproveTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasParams *GasParams, tokenAddr common.Address, amount *big.Int, erc20ABI *abi.ABI) (*types.Transaction, error) {
	data, err := erc20ABI.Pack("approve", common.HexToAddress(UNISWAP_V2_ROUTER_ADDR), amount)
	if err != nil {
		return nil, fmt.Errorf("failed to pack approve data: %v", err)
	}

	// Estimate gas
	gasLimit, err := estimateGasWithRetry(ctx, client, ethereum.CallMsg{
		From: crypto.PubkeyToAddress(key.PublicKey),
		To:   &tokenAddr,
		Data: data,
	}, 3)
	if err != nil {
		log.Printf("⚠️  Using default gas limit for approve: %v", err)
		gasLimit = getDefaultGasLimits("approve")
		gasLimit = gasLimit * (100 + GAS_LIMIT_BUFFER_PERCENT) / 100
	}

	// Create transaction based on gas type
	if gasParams.IsLegacy {
		tx := types.NewTransaction(nonce, tokenAddr, big.NewInt(0), gasLimit, gasParams.LegacyGasPrice, data)
		return types.SignTx(tx, types.NewEIP155Signer(chainID), key)
	} else {
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     nonce,
			GasTipCap: gasParams.MaxPriorityFee,
			GasFeeCap: gasParams.MaxFeePerGas,
			Gas:       gasLimit,
			To:        &tokenAddr,
			Value:     big.NewInt(0),
			Data:      data,
		})
		return types.SignTx(tx, types.NewLondonSigner(chainID), key)
	}
}

func createSwapTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, chainID *big.Int, to common.Address, nonce uint64, gasParams *GasParams, deadline, value, amountOutMin *big.Int, path []common.Address, routerABI *abi.ABI) (*types.Transaction, error) {
	data, err := routerABI.Pack("swapExactETHForTokens", amountOutMin, path, to, deadline)
	if err != nil {
		return nil, fmt.Errorf("failed to pack swap data: %v", err)
	}

	routerAddr := common.HexToAddress(UNISWAP_V2_ROUTER_ADDR)

	// Estimate gas
	gasLimit, err := estimateGasWithRetry(ctx, client, ethereum.CallMsg{
		From:  to,
		To:    &routerAddr,
		Value: value,
		Data:  data,
	}, 3)
	if err != nil {
		log.Printf("⚠️  Using default gas limit for swap: %v", err)
		gasLimit = getDefaultGasLimits("swap")
		gasLimit = gasLimit * (100 + GAS_LIMIT_BUFFER_PERCENT) / 100
	}

	// Create transaction based on gas type
	if gasParams.IsLegacy {
		tx := types.NewTransaction(nonce, routerAddr, value, gasLimit, gasParams.LegacyGasPrice, data)
		return types.SignTx(tx, types.NewEIP155Signer(chainID), key)
	} else {
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     nonce,
			GasTipCap: gasParams.MaxPriorityFee,
			GasFeeCap: gasParams.MaxFeePerGas,
			Gas:       gasLimit,
			To:        &routerAddr,
			Value:     value,
			Data:      data,
		})
		return types.SignTx(tx, types.NewLondonSigner(chainID), key)
	}
}

func createAddLiquidityTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, chainID *big.Int, to common.Address, nonce uint64, gasParams *GasParams, deadline *big.Int, tokenAddr common.Address, tokenAmount, ethAmount *big.Int, slippage float64, routerABI *abi.ABI) (*types.Transaction, error) {
	amountTokenMin := applySlippage(tokenAmount, slippage)
	amountETHMin := applySlippage(ethAmount, slippage)

	data, err := routerABI.Pack("addLiquidityETH", tokenAddr, tokenAmount, amountTokenMin, amountETHMin, to, deadline)
	if err != nil {
		return nil, fmt.Errorf("failed to pack add liquidity data: %v", err)
	}

	routerAddr := common.HexToAddress(UNISWAP_V2_ROUTER_ADDR)

	// Estimate gas
	gasLimit, err := estimateGasWithRetry(ctx, client, ethereum.CallMsg{
		From:  to,
		To:    &routerAddr,
		Value: ethAmount,
		Data:  data,
	}, 3)
	if err != nil {
		log.Printf("⚠️  Using default gas limit for addLiquidity: %v", err)
		gasLimit = getDefaultGasLimits("addLiquidity")
		gasLimit = gasLimit * (100 + GAS_LIMIT_BUFFER_PERCENT) / 100
	}

	// Create transaction based on gas type
	if gasParams.IsLegacy {
		tx := types.NewTransaction(nonce, routerAddr, ethAmount, gasLimit, gasParams.LegacyGasPrice, data)
		return types.SignTx(tx, types.NewEIP155Signer(chainID), key)
	} else {
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     nonce,
			GasTipCap: gasParams.MaxPriorityFee,
			GasFeeCap: gasParams.MaxFeePerGas,
			Gas:       gasLimit,
			To:        &routerAddr,
			Value:     ethAmount,
			Data:      data,
		})
		return types.SignTx(tx, types.NewLondonSigner(chainID), key)
	}
}

// #############################################################################
// ##                          FLASHBOTS INTEGRATION                          ##
// #############################################################################

func simulateBundle(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey) (*FlashbotsSimulationResponse, error) {
	// Encode transactions
	var txsHex []string
	for i, tx := range txs {
		rawTx, err := tx.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("failed to encode transaction: %v", err)
		}
		log.Printf("TX %d len=%d firstByte=%#x", i+1, len(rawTx), rawTx[0])

		var chk types.Transaction
		if err := chk.UnmarshalBinary(rawTx); err != nil {
			log.Fatalf("local decode failed: %v", err)
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
	for i, tx := range txs {
		rawTx, err := tx.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("failed to encode transaction: %v", err)
		}

		log.Printf("TX %d len=%d firstByte=%#x", i+1, len(rawTx), rawTx[0])

		txsHex = append(txsHex, hexutil.Encode(rawTx))

		var chk types.Transaction
		if err := chk.UnmarshalBinary(rawTx); err != nil {
			log.Fatalf("local decode failed: %v", err)
		}
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

func sendBundleWithRetries(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey, maxRetries int) (*FlashbotsSendResponse, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err := sendBundle(ctx, txs, authKey)
		if err == nil && result.Error == nil {
			if attempt > 1 {
				log.Printf("✅ Bundle sent successfully on attempt %d", attempt)
			}
			return result, nil
		}

		if result != nil && result.Error != nil {
			lastErr = fmt.Errorf("flashbots error: %s", result.Error.Message)
		} else {
			lastErr = err
		}

		if attempt < maxRetries {
			log.Printf("🔄 Bundle send attempt %d failed, retrying: %v", attempt, lastErr)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	return nil, fmt.Errorf("failed to send bundle after %d attempts: %v", maxRetries, lastErr)
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

// func signFlashbotsPayload(body []byte, key *ecdsa.PrivateKey) (string, error) {
// 	hash := crypto.Keccak256Hash(body)
// 	signature, err := crypto.Sign(hash.Bytes(), key)
// 	if err != nil {
// 		return "", fmt.Errorf("failed to sign: %v", err)
// 	}

// 	signature[64] += 27

// 	address := crypto.PubkeyToAddress(key.PublicKey)
// 	fmt.Println("📌 Flashbots signer address used:", address.Hex())

// 	return fmt.Sprintf("%s:0x%s", address.Hex(), hex.EncodeToString(signature)), nil
// }

func signFlashbotsPayload(body []byte, key *ecdsa.PrivateKey) (string, error) {
	// 1) Keccak-256 روی بدنهٔ خام
	rawHash := crypto.Keccak256(body)

	// 2) هَش را به رشتهٔ هگز (با 0x) تبدیل کنید
	hexHash := []byte(hexutil.Encode(rawHash))

	// 3) هش شخصی‌شدهٔ EIP-191
	prefixedHash := accounts.TextHash(hexHash)

	// 4) امضا
	sig, err := crypto.Sign(prefixedHash, key)
	if err != nil {
		return "", fmt.Errorf("sign error: %w", err)
	}
	if sig[64] < 27 { // هماهنگ با go-ethereum
		sig[64] += 27
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)
	return fmt.Sprintf("%s:%s", addr.Hex(), hexutil.Encode(sig)), nil
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

func gweiToWei(gwei float64) *big.Int {
	gweiFloat := big.NewFloat(gwei)
	weiFloat := new(big.Float).Mul(gweiFloat, big.NewFloat(params.GWei))
	wei, _ := weiFloat.Int(nil)
	return wei
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
	log.Printf("⏳ Monitoring bundle inclusion with fast polling (timeout: %v)...", timeout)

	startTime := time.Now()
	ticker := time.NewTicker(1 * time.Second) // Faster polling for quicker detection
	defer ticker.Stop()

	txHashes := make([]common.Hash, len(txs))
	for i, tx := range txs {
		txHashes[i] = tx.Hash()
	}

	includedCount := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout):
			return fmt.Errorf("bundle inclusion timeout after %v (included: %d/%d)", timeout, includedCount, len(txHashes))
		case <-ticker.C:
			// Check if any transaction is included
			newlyIncluded := 0
			for i, txHash := range txHashes {
				receipt, err := client.TransactionReceipt(ctx, txHash)
				if err == nil && receipt != nil && receipt.Status == 1 {
					if includedCount <= i {
						log.Printf("✅ Transaction %d included in block %d (status: success)", i+1, receipt.BlockNumber.Uint64())
						newlyIncluded++
					}
					if i+1 > includedCount {
						includedCount = i + 1
					}
				}
			}

			// Check if all transactions are included
			if includedCount == len(txHashes) {
				log.Printf("🎉 All transactions confirmed! Total time: %v", time.Since(startTime).Truncate(time.Millisecond))
				return nil
			}

			// Log progress every 5 seconds
			elapsed := time.Since(startTime)
			if elapsed.Truncate(time.Second).Seconds() > 0 && int(elapsed.Seconds())%5 == 0 {
				log.Printf("⏱️  Monitoring... elapsed: %v, included: %d/%d", elapsed.Truncate(time.Second), includedCount, len(txHashes))
			}
		}
	}
}

func init() {
	// Set up logging with timestamps
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
