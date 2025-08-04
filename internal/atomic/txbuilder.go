package atomic

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/nimazeighami/flash-liquswap-sync/internal/configs"
)

func applySlippage(amount *big.Int, slippagePercent float64) *big.Int {
	slippageMultiplier := big.NewFloat(1.0 - slippagePercent)
	amountFloat := new(big.Float).SetInt(amount)
	minAmountFloat := new(big.Float).Mul(amountFloat, slippageMultiplier)
	minAmount, _ := minAmountFloat.Int(nil)
	return minAmount
}

func createApproveTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasParams *GasParams, tokenAddr common.Address, amount *big.Int, erc20ABI *abi.ABI) (*types.Transaction, error) {
	data, err := erc20ABI.Pack("approve", common.HexToAddress(configs.UNISWAP_V2_ROUTER_ADDR), amount)
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
		gasLimit = gasLimit * (100 + configs.GAS_LIMIT_BUFFER_PERCENT) / 100
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

	routerAddr := common.HexToAddress(configs.UNISWAP_V2_ROUTER_ADDR)

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
		gasLimit = gasLimit * (100 + configs.GAS_LIMIT_BUFFER_PERCENT) / 100
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

	routerAddr := common.HexToAddress(configs.UNISWAP_V2_ROUTER_ADDR)

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
		gasLimit = gasLimit * (100 + configs.GAS_LIMIT_BUFFER_PERCENT) / 100
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
