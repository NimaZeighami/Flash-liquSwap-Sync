package flashbot
import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/nimazeighami/flash-liquswap-sync/internal/configs"
)


func SimulateBundle(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey) (*SimulationResponse, error) {
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
	client, _ := ethclient.Dial(configs.RPC_URL)
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

	request := Request{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "eth_callBundle",
		Params:  []interface{}{params},
	}

	return SendFlashbotsRequest[SimulationResponse](ctx, request, authKey)
}

func SendBundle(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey) (*SendResponse, error) {
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
	client, _ := ethclient.Dial(configs.RPC_URL)
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block: %v", err)
	}
	targetBlock := header.Number.Uint64() + 1

	// Prepare send request
	params := Bundle{
		Txs:         txsHex,
		BlockNumber: fmt.Sprintf("0x%x", targetBlock),
	}

	request := Request{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "eth_sendBundle",
		Params:  []interface{}{params},
	}

	return SendFlashbotsRequest[SendResponse](ctx, request, authKey)
}

func SendBundleWithRetries(ctx context.Context, txs []*types.Transaction, authKey *ecdsa.PrivateKey, maxRetries int) (*SendResponse, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err := SendBundle(ctx, txs, authKey)
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

func SendFlashbotsRequest[T any](ctx context.Context, request Request, authKey *ecdsa.PrivateKey) (*T, error) {
	// Marshal request
	reqBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", configs.FLASHBOTS_RELAY_URL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	// Sign request
	signature, err := SignFlashbotsPayload(reqBody, authKey)
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



func SignFlashbotsPayload(body []byte, key *ecdsa.PrivateKey) (string, error) {

	rawHash := crypto.Keccak256(body)

	hexHash := []byte(hexutil.Encode(rawHash))
	prefixedHash := accounts.TextHash(hexHash)
	sig, err := crypto.Sign(prefixedHash, key)
	if err != nil {
		return "", fmt.Errorf("sign error: %w", err)
	}
	if sig[64] < 27 {
		sig[64] += 27
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)
	return fmt.Sprintf("%s:%s", addr.Hex(), hexutil.Encode(sig)), nil
}

