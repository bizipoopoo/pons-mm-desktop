package pons

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestBuildV2Launch(t *testing.T) {
	signer, err := LoadSigner("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", big.NewInt(RobinhoodChainID))
	if err != nil {
		t.Fatal(err)
	}
	params := V2TokenParams{
		Name: "Example", Symbol: "EXMPL", Socials: V2Socials{Website: "https://example.com"},
		CreatorFeeRecipient: signer.Address(), BuybackEnabled: true,
		ExpectedEconomics: [32]byte{1}, Salt: [32]byte{2},
	}
	value := big.NewInt(500_000_000_000_000)
	tx, err := signer.BuildV2Launch(params, 0, common.Address{}, []common.Address{common.HexToAddress("0x1")}, value, TxParams{
		Nonce: 3, GasLimit: 3_000_000, TipCap: big.NewInt(1), FeeCap: big.NewInt(2),
	})
	if err != nil {
		t.Fatalf("BuildV2Launch: %v", err)
	}
	if tx.To() == nil || *tx.To() != common.HexToAddress(LaunchFactory) {
		t.Fatalf("tx destination = %v, want v2 factory", tx.To())
	}
	if tx.Value().Cmp(value) != 0 {
		t.Fatalf("tx value = %s, want %s", tx.Value(), value)
	}
	method := factoryABI.Methods["launchToken"]
	if len(tx.Data()) < 4 || string(tx.Data()[:4]) != string(method.ID) {
		t.Fatalf("wrong launchToken selector: %x", tx.Data())
	}
}

func TestV2LaunchedFromReceipt(t *testing.T) {
	token := common.HexToAddress("0x1111111111111111111111111111111111111111")
	curve := common.HexToAddress("0x2222222222222222222222222222222222222222")
	deployer := common.HexToAddress("0x3333333333333333333333333333333333333333")
	pairToken := common.Address{}
	launchConfigID := big.NewInt(7)
	graduationThreshold := big.NewInt(4_200_000_000_000_000_000)
	data, err := factoryABI.Events["TokenLaunched"].Inputs.NonIndexed().Pack(
		pairToken, launchConfigID, graduationThreshold,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &types.Receipt{Logs: []*types.Log{{
		Address: common.HexToAddress(LaunchFactory),
		Topics: []common.Hash{
			tokenLaunchedTopic,
			common.BytesToHash(token.Bytes()),
			common.BytesToHash(curve.Bytes()),
			common.BytesToHash(deployer.Bytes()),
		},
		Data: data,
	}}}

	launch, ok := V2LaunchedFromReceipt(receipt)
	if !ok {
		t.Fatal("v2 TokenLaunched event did not decode")
	}
	if launch.Token != token || launch.Curve != curve || launch.Deployer != deployer || launch.PairToken != pairToken {
		t.Fatalf("decoded addresses are wrong: %+v", launch)
	}
	if launch.LaunchConfigID.Cmp(launchConfigID) != 0 || launch.GraduationThreshold.Cmp(graduationThreshold) != 0 {
		t.Fatalf("decoded launch economics are wrong: %+v", launch)
	}
}

func TestDecodeCurveTrade(t *testing.T) {
	buyer := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	quoteIn, tokensOut := big.NewInt(100), big.NewInt(250)
	data, err := curveABI.Events["CurveBuy"].Inputs.NonIndexed().Pack(quoteIn, tokensOut, big.NewInt(1), big.NewInt(2))
	if err != nil {
		t.Fatal(err)
	}
	hash := common.HexToHash("0x1234")
	trade, ok := decodeCurveTrade(types.Log{
		Topics: []common.Hash{curveBuyTopic, common.BytesToHash(buyer.Bytes()), common.BytesToHash(recipient.Bytes())},
		Data:   data, TxHash: hash, BlockNumber: 42, Index: 7,
	})
	if !ok {
		t.Fatal("curve buy did not decode")
	}
	if !trade.IsBuy || trade.Trader != buyer || trade.Recipient != recipient || trade.TxHash != hash {
		t.Fatalf("decoded metadata is wrong: %+v", trade)
	}
	if trade.Block != 42 || trade.LogIndex != 7 {
		t.Fatalf("decoded log position = %d/%d, want 42/7", trade.Block, trade.LogIndex)
	}
	if trade.QuoteAmount.Cmp(quoteIn) != 0 || trade.TokenAmount.Cmp(tokensOut) != 0 {
		t.Fatalf("decoded amounts are wrong: %+v", trade)
	}
}
