package main

import (
	"context"
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"

	"github.com/nimazeighami/flash-liquswap-sync/internal/configs"
	"github.com/nimazeighami/flash-liquswap-sync/internal/atomic"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func main() {
	log.Println("🚀 Flashbots Atomic Uniswap V2 Operations (Dynamic Gas)")
	log.Println("====================================================")

	// Parse configuration
	config, err := configs.ParseConfig()
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
	gasParams, err := atomic.CalculateDynamicGasParams(ctx, client)
	if err != nil {
		log.Fatalf("Failed to calculate gas parameters: %v", err)
	}

	if gasParams.IsLegacy {
		log.Printf("⛽ Using legacy gas: %s Gwei", atomic.WeiToGwei(gasParams.LegacyGasPrice).Text('f', 2))
	} else {
		log.Printf("⛽ Using EIP-1559: MaxFee=%s Gwei, PriorityFee=%s Gwei",
			atomic.WeiToGwei(gasParams.MaxFeePerGas).Text('f', 2),
			atomic.WeiToGwei(gasParams.MaxPriorityFee).Text('f', 2))
	}

	log.Printf("Chain ID: %s, Nonce: %d", chainID.String(), nonce)

	// Display transaction plan
	ethFloat := new(big.Float).Quo(new(big.Float).SetInt(config.EthAmount), big.NewFloat(params.Ether))
	log.Printf("📋 Transaction Plan:")
	log.Printf("   • Swap %s ETH → %s", ethFloat.Text('f', 6), config.TokenAddress.Hex())
	log.Printf("   • Add liquidity with received tokens + remaining ETH")
	log.Printf("   • Slippage tolerance: %.2f%%", config.SlippageTolerance*100)

	// Execute atomic operations
	if err := atomic.ExecuteAtomicOperations(ctx, client, config, eoaKey, flashbotsKey, chainID, nonce, gasParams); err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	log.Println("🎉 Atomic operations completed successfully!")
}
