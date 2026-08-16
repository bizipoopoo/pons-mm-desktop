package pons

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Signer holds an ECDSA key and turns pons calls into signed EIP-1559
// transactions for Robinhood Chain.
type Signer struct {
	key     *ecdsa.PrivateKey
	addr    common.Address
	chainID *big.Int
}

// LoadSigner builds a Signer from a hex private key (with or without 0x) for the
// given chain id.
func LoadSigner(hexKey string, chainID *big.Int) (*Signer, error) {
	hexKey = strings.TrimSpace(hexKey)
	hexKey = strings.TrimPrefix(hexKey, "0x")
	if _, err := hex.DecodeString(hexKey); err != nil {
		return nil, fmt.Errorf("private key is not valid hex: %w", err)
	}
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return &Signer{key: key, addr: crypto.PubkeyToAddress(key.PublicKey), chainID: chainID}, nil
}

// LoadSignerFromFile reads a hex private key from a file (trimming whitespace).
func LoadSignerFromFile(path string, chainID *big.Int) (*Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	return LoadSigner(string(b), chainID)
}

// Address is the signer's public address (the payer / recipient).
func (s *Signer) Address() common.Address { return s.addr }

// TxParams carries the gas and nonce settings shared by every built tx.
type TxParams struct {
	Nonce    uint64
	GasLimit uint64
	TipCap   *big.Int // maxPriorityFeePerGas
	FeeCap   *big.Int // maxFeePerGas
}

// sign builds and signs an EIP-1559 tx to `to` carrying value and data.
func (s *Signer) sign(to common.Address, value *big.Int, data []byte, p TxParams) (*types.Transaction, error) {
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   s.chainID,
		Nonce:     p.Nonce,
		GasTipCap: p.TipCap,
		GasFeeCap: p.FeeCap,
		Gas:       p.GasLimit,
		To:        &to,
		Value:     value,
		Data:      data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(s.chainID), s.key)
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}
	return signed, nil
}

// BuildBuy signs a curve.buy(quoteIn, minTokensOut, recipient). For a native
// ETH launch the tx value equals quoteIn; a custom-pair launch must approve the
// curve first and send zero value (handled by the caller).
func (s *Signer) BuildBuy(curve common.Address, quoteIn, minTokensOut *big.Int, native bool, p TxParams) (*types.Transaction, error) {
	data, err := curveABI.Pack("buy", quoteIn, minTokensOut, s.addr)
	if err != nil {
		return nil, fmt.Errorf("pack buy: %w", err)
	}
	value := big.NewInt(0)
	if native {
		value = quoteIn
	}
	return s.sign(curve, value, data, p)
}

// BuildSell signs a curve.sell(tokensIn, minQuoteOut, recipient).
func (s *Signer) BuildSell(curve common.Address, tokensIn, minQuoteOut *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := curveABI.Pack("sell", tokensIn, minQuoteOut, s.addr)
	if err != nil {
		return nil, fmt.Errorf("pack sell: %w", err)
	}
	return s.sign(curve, big.NewInt(0), data, p)
}

// BuildApprove signs an ERC-20 approve(spender, amount) on the launch token, so
// the curve can pull tokens on a custom-pair sell.
func (s *Signer) BuildApprove(token, spender common.Address, amount *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := erc20ABI.Pack("approve", spender, amount)
	if err != nil {
		return nil, fmt.Errorf("pack approve: %w", err)
	}
	return s.sign(token, big.NewInt(0), data, p)
}

// BuildTransfer signs a plain native-ETH transfer of value to `to` (no calldata).
func (s *Signer) BuildTransfer(to common.Address, value *big.Int, p TxParams) (*types.Transaction, error) {
	return s.sign(to, value, nil, p)
}

// Send broadcasts a signed tx.
func (c *Client) Send(ctx context.Context, tx *types.Transaction) error {
	return c.eth.SendTransaction(ctx, tx)
}
