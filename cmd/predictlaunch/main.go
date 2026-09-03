// Command predictlaunch predicts a pons v2 token/curve address, optionally
// simulates launchToken, and optionally broadcasts a real launch to verify.
//
//	RPC=https://… PRIVATE_KEY=0x… go run ./cmd/predictlaunch
//	RPC=https://… PRIVATE_KEY=0x… SEND=1 go run ./cmd/predictlaunch
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

func main() {
	rpc := os.Getenv("RPC")
	if rpc == "" {
		rpc = pons.DefaultRPC
	}
	key := strings.TrimPrefix(os.Getenv("PRIVATE_KEY"), "0x")
	send := os.Getenv("SEND") == "1"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client, err := pons.Dial(ctx, rpc)
	if err != nil {
		fatal(err)
	}
	defer client.Close()
	if client.ChainID().Cmp(big.NewInt(pons.RobinhoodChainID)) != 0 {
		fatal(fmt.Errorf("wrong chain %v", client.ChainID()))
	}

	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		fatal(err)
	}
	fee, err := client.V2LaunchFee(ctx)
	if err != nil {
		fatal(err)
	}
	economics, err := client.PreviewV2LaunchEconomics(ctx, 0, common.Address{})
	if err != nil {
		fatal(err)
	}

	// Prefer a funded on-chain account as the simulated deployer so Nitro's
	// eth_call balance check for msg.value succeeds without state overrides.
	deployer := common.HexToAddress("0xd439325794932c3ccd45affa85effe5363af1ca8")
	var signer *pons.Signer
	if key != "" {
		signer, err = pons.LoadSigner(key, client.ChainID())
		if err != nil {
			fatal(err)
		}
		deployer = signer.Address()
	}

	params := pons.V2TokenParams{
		Name: "PredictProbe", Symbol: "PRED",
		Logo: "https://example.com/pred.png", Description: "address prediction probe",
		CreatorFeeRecipient: deployer,
		BuybackEnabled:      true,
		ExpectedEconomics:   economics,
		Salt:                salt,
	}

	fmt.Println("deployer wallet:", deployer.Hex())
	fmt.Println("salt:", common.Bytes2Hex(salt[:]))
	fmt.Println("launch fee wei:", fee)

	token, curve, err := client.PredictV2LaunchAddresses(ctx, params, 0, common.Address{}, deployer)
	if err != nil {
		fatal(fmt.Errorf("predict: %w", err))
	}
	fmt.Println("predicted token:", token.Hex())
	fmt.Println("predicted curve:", curve.Hex())

	// Cross-check: eth_call launchToken with the same terms (no broadcast).
	data, err := packLaunch(params)
	if err != nil {
		fatal(err)
	}
	factory := common.HexToAddress(pons.LaunchFactory)
	sim, err := client.Eth().CallContract(ctx, ethereum.CallMsg{
		From: deployer, To: &factory, Value: fee, Data: data,
	}, nil)
	if err != nil {
		fatal(fmt.Errorf("eth_call launchToken: %w", err))
	}
	if len(sim) < 64 {
		fatal(fmt.Errorf("eth_call returned %d bytes", len(sim)))
	}
	simToken := common.BytesToAddress(sim[12:32])
	simCurve := common.BytesToAddress(sim[44:64])
	fmt.Println("eth_call token:", simToken.Hex())
	fmt.Println("eth_call curve:", simCurve.Hex())
	if simToken != token || simCurve != curve {
		fatal(fmt.Errorf("predict mismatch vs eth_call"))
	}
	fmt.Println("predict matches eth_call ✓")

	if signer != nil {
		can, err := client.CanLaunchV2(ctx, deployer)
		if err != nil {
			fatal(err)
		}
		fmt.Println("canLaunch:", can)
	}

	if !send {
		fmt.Println("dry-run only (set SEND=1 PRIVATE_KEY=… to broadcast)")
		return
	}
	if signer == nil {
		fatal(fmt.Errorf("SEND=1 requires PRIVATE_KEY"))
	}

	client.WarmGas(ctx)
	tip, feeCap, err := client.SuggestGas(ctx, nil)
	if err != nil {
		fatal(err)
	}
	nonce, err := client.PendingNonce(ctx, deployer)
	if err != nil {
		fatal(err)
	}
	tx, err := signer.BuildV2Launch(params, 0, common.Address{}, nil, fee, pons.TxParams{
		Nonce: nonce, GasLimit: 6_000_000, TipCap: tip, FeeCap: feeCap,
	})
	if err != nil {
		fatal(err)
	}
	if err := client.Send(ctx, tx); err != nil {
		fatal(err)
	}
	fmt.Println("submitted:", tx.Hash().Hex())
	rcpt, err := client.WaitReceipt(ctx, tx.Hash(), 2*time.Minute)
	if err != nil {
		fatal(err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		fatal(fmt.Errorf("launch reverted"))
	}
	launched, ok := pons.V2LaunchedFromReceipt(rcpt)
	if !ok {
		fatal(fmt.Errorf("no TokenLaunched event"))
	}
	fmt.Println("on-chain token:", launched.Token.Hex())
	fmt.Println("on-chain curve:", launched.Curve.Hex())
	fmt.Println("block:", launched.Block)
	if launched.Token != token || launched.Curve != curve {
		fatal(fmt.Errorf("on-chain addresses differ from prediction"))
	}
	fmt.Println("predict matches on-chain launch ✓")
}

func packLaunch(params pons.V2TokenParams) ([]byte, error) {
	// Reuse BuildV2Launch encoding by signing a throwaway tx and taking data —
	// cheaper than re-exporting the ABI pack. Gas fields are irrelevant.
	signer, err := pons.LoadSigner(strings.Repeat("11", 32), big.NewInt(pons.RobinhoodChainID))
	if err != nil {
		return nil, err
	}
	tx, err := signer.BuildV2Launch(params, 0, common.Address{}, nil, big.NewInt(0), pons.TxParams{
		Nonce: 0, GasLimit: 1, TipCap: big.NewInt(1), FeeCap: big.NewInt(1),
	})
	if err != nil {
		return nil, err
	}
	return tx.Data(), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
