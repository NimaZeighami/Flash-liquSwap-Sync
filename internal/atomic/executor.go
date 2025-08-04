package atomic

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/nimazeighami/flash-liquswap-sync/internal/configs"
	"github.com/nimazeighami/flash-liquswap-sync/internal/flashbot"
)

func formatTokenAmount(amount *big.Int, decimals int) string {
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	tokenFloat := new(big.Float).Quo(new(big.Float).SetInt(amount), new(big.Float).SetInt(divisor))
	return tokenFloat.Text('f', 6)
}

func getAmountsOut(ctx context.Context, client *ethclient.Client, routerABI *abi.ABI, amountIn *big.Int, path []common.Address) (*big.Int, error) {
	data, err := routerABI.Pack("getAmountsOut", amountIn, path)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getAmountsOut: %v", err)
	}

	routerAddr := common.HexToAddress(configs.UNISWAP_V2_ROUTER_ADDR)
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

func ExecuteAtomicOperations(ctx context.Context, client *ethclient.Client, config *configs.Config, eoaKey, flashbotsKey *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasParams *GasParams) error {
	eoaAddress := crypto.PubkeyToAddress(eoaKey.PublicKey)
	deadline := big.NewInt(time.Now().Unix() + config.DeadlineSeconds)

	// Parse ABIs
	routerContractABI, err := abi.JSON(strings.NewReader(configs.RouterABI))
	if err != nil {
		return fmt.Errorf("failed to parse router ABI: %v", err)
	}

	erc20ContractABI, err := abi.JSON(strings.NewReader(configs.Erc20ABI))
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
	path := []common.Address{common.HexToAddress(configs.WETH_ADDRESS), config.TokenAddress}
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
	log.Printf("📊 Bundle Stats: Total Gas=%d, Est. Fees=~%s ETH", totalGasUsed, WeiToEth(totalFees.String()))

	// Simulate bundle first
	simResult, err := flashbot.SimulateBundle(ctx, transactions, flashbotsKey)
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
			log.Printf("   TX %d: Gas used %s, Gas fees %s ETH", i+1, result.GasUsed, WeiToEth(result.GasFees))
		}
	}

	// Send bundle with retries for better inclusion chance
	sendResult, err := flashbot.SendBundleWithRetries(ctx, transactions, flashbotsKey, 3)
	if err != nil {
		return fmt.Errorf("failed to send bundle: %v", err)
	}

	log.Printf("🎯 Bundle submitted! Hash: %s", sendResult.Result.BundleHash)

	// Monitor for inclusion with faster polling
	return monitorBundleInclusion(ctx, client, transactions, 60*time.Second)
}
