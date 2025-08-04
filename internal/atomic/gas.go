// atomic package provides utilities for handling gas estimation and transaction creation for atomic operations
// gas.go contains functions to calculate dynamic gas parameters, estimate gas with retries, and handle slippage.
package atomic

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"

	"github.com/nimazeighami/flash-liquswap-sync/internal/configs"
)

type GasParams struct {
	GasLimit       uint64
	MaxFeePerGas   *big.Int
	MaxPriorityFee *big.Int
	IsLegacy       bool
	LegacyGasPrice *big.Int
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

func WeiToGwei(wei *big.Int) *big.Float {
	return new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(params.GWei))
}

func GweiToWei(gwei float64) *big.Int {
	gweiFloat := big.NewFloat(gwei)
	weiFloat := new(big.Float).Mul(gweiFloat, big.NewFloat(params.GWei))
	wei, _ := weiFloat.Int(nil)
	return wei
}

func WeiToEth(weiStr string) string {
	wei, ok := new(big.Int).SetString(weiStr, 10)
	if !ok {
		return "0"
	}
	ethFloat := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(params.Ether))
	return ethFloat.Text('f', 6)
}

func CalculateDynamicGasParams(ctx context.Context, client *ethclient.Client) (*GasParams, error) {
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
		priorityFee = GweiToWei(configs.MIN_PRIORITY_FEE_GWEI)
	}

	// Apply multiplier for faster inclusion
	priorityFee = new(big.Int).Mul(priorityFee, big.NewInt(int64(configs.PRIORITY_FEE_MULTIPLIER*100)))
	priorityFee = new(big.Int).Div(priorityFee, big.NewInt(100))

	// Enforce min/max bounds
	minPriorityFee := GweiToWei(configs.MIN_PRIORITY_FEE_GWEI)
	maxPriorityFee := GweiToWei(configs.MAX_PRIORITY_FEE_GWEI)

	if priorityFee.Cmp(minPriorityFee) < 0 {
		priorityFee = minPriorityFee
	}
	if priorityFee.Cmp(maxPriorityFee) > 0 {
		priorityFee = maxPriorityFee
	}

	// Calculate maxFeePerGas = (baseFee * multiplier) + priorityFee
	maxBaseFee := new(big.Float).Mul(new(big.Float).SetInt(baseFee), big.NewFloat(configs.BASE_FEE_MULTIPLIER))
	maxBaseFeeInt, _ := maxBaseFee.Int(nil)
	maxFeePerGas := new(big.Int).Add(maxBaseFeeInt, priorityFee)

	log.Printf("🔥 Gas Market Analysis:")
	log.Printf("   • Current Base Fee: %s Gwei", WeiToGwei(baseFee).Text('f', 2))
	log.Printf("   • Dynamic Priority Fee: %s Gwei", WeiToGwei(priorityFee).Text('f', 2))
	log.Printf("   • Max Fee Per Gas: %s Gwei", WeiToGwei(maxFeePerGas).Text('f', 2))

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
			bufferedGas := gasLimit * (100 + configs.GAS_LIMIT_BUFFER_PERCENT) / 100
			return bufferedGas, nil
		}

		lastErr = err
		if i < retries-1 {
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		}
	}

	return 0, fmt.Errorf("gas estimation failed after %d retries: %v", retries, lastErr)
}
