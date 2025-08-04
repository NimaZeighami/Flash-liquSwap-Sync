package configs

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

const (
	// -- Endpoints --
	RPC_URL             = "https://eth.llamarpc.com"
	FLASHBOTS_RELAY_URL = "https://relay.flashbots.net"

	// -- Contract Addresses (Mainnet) --
	UNISWAP_V2_ROUTER_ADDR = "0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D"
	WETH_ADDRESS           = "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"

	// -- Default Parameters --
	DEFAULT_ETH_AMOUNT       = "0.002" // ETH to swap
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
	RouterABI = `[
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

	Erc20ABI = `[
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


type Config struct {
	RpcURL             string
	EoaPrivateKey      string
	FlashbotsSignerKey string
	EthAmount          *big.Int
	TokenAddress       common.Address
	SlippageTolerance  float64
	DeadlineSeconds    int64
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

func ParseConfig() (*Config, error) {
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