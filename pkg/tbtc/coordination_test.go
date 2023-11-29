package tbtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/go-test/deep"
	"github.com/keep-network/keep-core/pkg/bitcoin"
	"github.com/keep-network/keep-core/pkg/chain"
	"github.com/keep-network/keep-core/pkg/chain/local_v1"
	"github.com/keep-network/keep-core/pkg/net"
	netlocal "github.com/keep-network/keep-core/pkg/net/local"
	"github.com/keep-network/keep-core/pkg/operator"
	"github.com/keep-network/keep-core/pkg/protocol/group"
	"github.com/keep-network/keep-core/pkg/tecdsa"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/keep-network/keep-core/internal/testutils"
)

func TestCoordinationWindow_ActivePhaseEndBlock(t *testing.T) {
	window := newCoordinationWindow(900)

	testutils.AssertIntsEqual(
		t,
		"active phase end block",
		980,
		int(window.activePhaseEndBlock()),
	)
}

func TestCoordinationWindow_EndBlock(t *testing.T) {
	window := newCoordinationWindow(900)

	testutils.AssertIntsEqual(
		t,
		"end block",
		1000,
		int(window.endBlock()),
	)
}

func TestCoordinationWindow_IsAfterActivePhase(t *testing.T) {
	window := newCoordinationWindow(1800)

	previousWindow := newCoordinationWindow(900)
	sameWindow := newCoordinationWindow(1800)
	nextWindow := newCoordinationWindow(2700)

	testutils.AssertBoolsEqual(
		t,
		"result for nil",
		true,
		window.isAfter(nil),
	)
	testutils.AssertBoolsEqual(
		t,
		"result for previous window",
		true,
		window.isAfter(previousWindow),
	)
	testutils.AssertBoolsEqual(
		t,
		"result for same window",
		false,
		window.isAfter(sameWindow),
	)
	testutils.AssertBoolsEqual(
		t,
		"result for next window",
		false,
		window.isAfter(nextWindow),
	)
}

func TestCoordinationWindow_Index(t *testing.T) {
	tests := map[string]struct {
		coordinationBlock uint64
		expectedIndex     uint64
	}{
		"block 0": {
			coordinationBlock: 0,
			expectedIndex:     0,
		},
		"block 900": {
			coordinationBlock: 900,
			expectedIndex:     1,
		},
		"block 1800": {
			coordinationBlock: 1800,
			expectedIndex:     2,
		},
		"block 9000": {
			coordinationBlock: 9000,
			expectedIndex:     10,
		},
		"block 9001": {
			coordinationBlock: 9001,
			expectedIndex:     0,
		},
	}

	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			window := newCoordinationWindow(test.coordinationBlock)

			testutils.AssertIntsEqual(
				t,
				"index",
				int(test.expectedIndex),
				int(window.index()),
			)
		})
	}
}

func TestWatchCoordinationWindows(t *testing.T) {
	watchBlocksFn := func(ctx context.Context) <-chan uint64 {
		blocksChan := make(chan uint64)

		go func() {
			ticker := time.NewTicker(1 * time.Millisecond)
			defer ticker.Stop()

			block := uint64(0)

			for {
				select {
				case <-ticker.C:
					block++
					blocksChan <- block
				case <-ctx.Done():
					return
				}
			}
		}()

		return blocksChan
	}

	receivedWindows := make([]*coordinationWindow, 0)
	onWindowFn := func(window *coordinationWindow) {
		receivedWindows = append(receivedWindows, window)
	}

	ctx, cancelCtx := context.WithTimeout(
		context.Background(),
		2000*time.Millisecond,
	)
	defer cancelCtx()

	go watchCoordinationWindows(ctx, watchBlocksFn, onWindowFn)

	<-ctx.Done()

	testutils.AssertIntsEqual(t, "received windows", 2, len(receivedWindows))
	testutils.AssertIntsEqual(
		t,
		"first window",
		900,
		int(receivedWindows[0].coordinationBlock),
	)
	testutils.AssertIntsEqual(
		t,
		"second window",
		1800,
		int(receivedWindows[1].coordinationBlock),
	)
}

func TestCoordinationExecutor_CoordinationSeed(t *testing.T) {
	coordinationBlock := uint64(900)

	localChain := Connect()

	localChain.setBlockHashByNumber(
		coordinationBlock-32,
		"1322996cbcbc38fc924a46f4df5f9064279d3ab43396e58386dac9b87440d64f",
	)

	// Uncompressed public key corresponding to the 20-byte public key hash:
	// aa768412ceed10bd423c025542ca90071f9fb62d.
	publicKeyHex, err := hex.DecodeString(
		"0471e30bca60f6548d7b42582a478ea37ada63b402af7b3ddd57f0c95bb6843175" +
			"aa0d2053a91a050a6797d85c38f2909cb7027f2344a01986aa2f9f8ca7a0c289",
	)
	if err != nil {
		t.Fatal(err)
	}

	coordinatedWallet := wallet{
		// Set only relevant fields.
		publicKey: unmarshalPublicKey(publicKeyHex),
	}

	executor := &coordinationExecutor{
		// Set only relevant fields.
		chain:             localChain,
		coordinatedWallet: coordinatedWallet,
	}

	seed, err := executor.coordinationSeed(coordinationBlock)
	if err != nil {
		t.Fatal(err)
	}

	// Expected seed is sha256(wallet_public_key_hash | safe_block_hash).
	expectedSeed := "e55c779d6d83183409ddc90c6cd5130567f0593349a9c82494b402048ec2d03d"

	testutils.AssertStringsEqual(
		t,
		"coordination seed",
		expectedSeed,
		hex.EncodeToString(seed[:]),
	)
}

func TestCoordinationExecutor_CoordinationLeader(t *testing.T) {
	seedBytes, err := hex.DecodeString(
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	)
	if err != nil {
		t.Fatal(err)
	}

	var seed [32]byte
	copy(seed[:], seedBytes)

	coordinatedWallet := wallet{
		// Set only relevant fields.
		signingGroupOperators: []chain.Address{
			"957ECF59507a6A74b8d98747f07a74De270D3CC3", // member 1
			"5E14c0f27612fbfB7A6FE40b5A6Ec997fA62fc04", // member 2
			"D2662604f8b4540336fBd3c1F48d7e9cdFbD079c", // member 3
			"7CBD87ABC182216A7Aa0E8d19aA21abFA2511383", // member 4
			"FAc73b03884d94a08a5c6c7BB12Ac0b20571F162", // member 5
			"705C76445651530fe0D25eeE287b6164cE2c7216", // member 6
			"7CBD87ABC182216A7Aa0E8d19aA21abFA2511383", // member 7  (same operator as member 4)
			"405ad1f632b49A0617fbdc1fD427aF54BA9Bb3dd", // member 8
			"7CBD87ABC182216A7Aa0E8d19aA21abFA2511383", // member 9  (same operator as member 4)
			"5E14c0f27612fbfB7A6FE40b5A6Ec997fA62fc04", // member 10 (same operator as member 2)
		},
	}

	executor := &coordinationExecutor{
		// Set only relevant fields.
		coordinatedWallet: coordinatedWallet,
	}

	leader := executor.coordinationLeader(seed)

	testutils.AssertStringsEqual(
		t,
		"coordination leader",
		"D2662604f8b4540336fBd3c1F48d7e9cdFbD079c",
		leader.String(),
	)
}

func TestCoordinationExecutor_ActionsChecklist(t *testing.T) {
	tests := map[string]struct {
		coordinationBlock uint64
		expectedChecklist []WalletActionType
	}{
		// Incorrect coordination window.
		"block 0": {
			coordinationBlock: 0,
			expectedChecklist: nil,
		},
		"block 900": {
			coordinationBlock: 900,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		// Incorrect coordination window.
		"block 901": {
			coordinationBlock: 901,
			expectedChecklist: nil,
		},
		"block 1800": {
			coordinationBlock: 1800,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 2700": {
			coordinationBlock: 2700,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 3600": {
			coordinationBlock: 3600,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 4500": {
			coordinationBlock: 4500,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		// Heartbeat randomly selected for the 6th coordination window.
		"block 5400": {
			coordinationBlock: 5400,
			expectedChecklist: []WalletActionType{
				ActionRedemption,
				ActionHeartbeat,
			},
		},
		"block 6300": {
			coordinationBlock: 6300,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 7200": {
			coordinationBlock: 7200,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 8100": {
			coordinationBlock: 8100,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 9000": {
			coordinationBlock: 9000,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 9900": {
			coordinationBlock: 9900,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 10800": {
			coordinationBlock: 10800,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		"block 11700": {
			coordinationBlock: 11700,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		// Heartbeat randomly selected for the 14th coordination window.
		"block 12600": {
			coordinationBlock: 12600,
			expectedChecklist: []WalletActionType{
				ActionRedemption,
				ActionHeartbeat,
			},
		},
		"block 13500": {
			coordinationBlock: 13500,
			expectedChecklist: []WalletActionType{ActionRedemption},
		},
		// 16th coordination window so, all actions should be on the checklist.
		"block 14400": {
			coordinationBlock: 14400,
			expectedChecklist: []WalletActionType{
				ActionRedemption,
				ActionDepositSweep,
				ActionMovedFundsSweep,
				ActionMovingFunds,
			},
		},
	}

	executor := &coordinationExecutor{}

	for testName, test := range tests {
		t.Run(
			testName, func(t *testing.T) {
				window := newCoordinationWindow(test.coordinationBlock)

				// Build an arbitrary seed based on the coordination block number.
				seed := sha256.Sum256(
					big.NewInt(int64(window.coordinationBlock) + 1).Bytes(),
				)

				checklist := executor.actionsChecklist(window.index(), seed)

				if diff := deep.Equal(
					checklist,
					test.expectedChecklist,
				); diff != nil {
					t.Errorf(
						"compare failed: %v\nactual: %s\nexpected: %s",
						diff,
						checklist,
						test.expectedChecklist,
					)
				}
			},
		)
	}
}

func TestCoordinationExecutor_LeaderRoutine(t *testing.T) {
	// Uncompressed public key corresponding to the 20-byte public key hash:
	// aa768412ceed10bd423c025542ca90071f9fb62d.
	publicKeyHex, err := hex.DecodeString(
		"0471e30bca60f6548d7b42582a478ea37ada63b402af7b3ddd57f0c95bb6843175" +
			"aa0d2053a91a050a6797d85c38f2909cb7027f2344a01986aa2f9f8ca7a0c289",
	)
	if err != nil {
		t.Fatal(err)
	}

	// 20-byte public key hash corresponding to the public key above.
	buffer, err := hex.DecodeString("aa768412ceed10bd423c025542ca90071f9fb62d")
	if err != nil {
		t.Fatal(err)
	}
	var publicKeyHash [20]byte
	copy(publicKeyHash[:], buffer)

	coordinatedWallet := wallet{
		// Set only relevant fields.
		publicKey: unmarshalPublicKey(publicKeyHex),
	}

	// Deliberately use an unsorted list of members indexes to make sure the
	// leader routine sorts them before determining the coordination message
	// sender.
	membersIndexes := []group.MemberIndex{77, 5, 10}

	proposalGenerator := func(
		walletPublicKeyHash [20]byte,
		actionsChecklist []WalletActionType,
	) (
		coordinationProposal,
		error,
	) {
		for _, action := range actionsChecklist {
			if walletPublicKeyHash == publicKeyHash && action == ActionHeartbeat {
				return &HeartbeatProposal{
					Message: []byte("heartbeat message"),
				}, nil
			}
		}

		return &noopProposal{}, nil
	}

	provider := netlocal.Connect()

	broadcastChannel, err := provider.BroadcastChannelFor("test")
	if err != nil {
		t.Fatal(err)
	}

	broadcastChannel.SetUnmarshaler(func() net.TaggedUnmarshaler {
		return &coordinationMessage{}
	})

	executor := &coordinationExecutor{
		// Set only relevant fields.
		coordinatedWallet: coordinatedWallet,
		membersIndexes:    membersIndexes,
		proposalGenerator: proposalGenerator,
		broadcastChannel:  broadcastChannel,
	}

	actionsChecklist := []WalletActionType{
		ActionRedemption,
		ActionDepositSweep,
		ActionMovedFundsSweep,
		ActionMovingFunds,
		ActionHeartbeat,
	}

	ctx, cancelCtx := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCtx()

	var message *coordinationMessage
	broadcastChannel.Recv(ctx, func(m net.Message) {
		cm, ok := m.Payload().(*coordinationMessage)
		if !ok {
			t.Fatal("unexpected message type")
		}

		// Capture the message for later assertions.
		message = cm

		// Cancel the context to proceed with the test quicker.
		cancelCtx()
	})

	proposal, err := executor.leaderRoutine(ctx, 900, actionsChecklist)
	if err != nil {
		t.Fatal(err)
	}

	<-ctx.Done()

	expectedProposal := &HeartbeatProposal{
		Message: []byte("heartbeat message"),
	}

	if !reflect.DeepEqual(expectedProposal, proposal) {
		t.Errorf(
			"unexpected proposal: \n"+
				"expected: %v\n"+
				"actual:   %v",
			expectedProposal,
			proposal,
		)
	}

	expectedMessage := &coordinationMessage{
		senderID:            5,
		coordinationBlock:   900,
		walletPublicKeyHash: publicKeyHash,
		proposal:            expectedProposal,
	}

	if !reflect.DeepEqual(expectedMessage, message) {
		t.Errorf(
			"unexpected message: \n"+
				"expected: %v\n"+
				"actual:   %v",
			expectedMessage,
			message,
		)
	}
}

func TestCoordinationExecutor_FollowerRoutine(t *testing.T) {
	// Uncompressed public key corresponding to the 20-byte public key hash:
	// aa768412ceed10bd423c025542ca90071f9fb62d.
	publicKeyHex, err := hex.DecodeString(
		"0471e30bca60f6548d7b42582a478ea37ada63b402af7b3ddd57f0c95bb6843175" +
			"aa0d2053a91a050a6797d85c38f2909cb7027f2344a01986aa2f9f8ca7a0c289",
	)
	if err != nil {
		t.Fatal(err)
	}

	parseScript := func(script string) bitcoin.Script {
		parsed, err := hex.DecodeString(script)
		if err != nil {
			t.Fatal(err)
		}

		return parsed
	}

	generateOperator := func() struct{
		address chain.Address
		channel net.BroadcastChannel
	} {
		operatorPrivateKey, operatorPublicKey, err := operator.GenerateKeyPair(
			local_v1.DefaultCurve,
		)
		if err != nil {
			t.Fatal(err)
		}

		operatorAddress, err := ConnectWithKey(operatorPrivateKey).
			Signing().
			PublicKeyToAddress(operatorPublicKey)
		if err != nil {
			t.Fatal(err)
		}

		provider := netlocal.ConnectWithKey(operatorPublicKey)
		broadcastChannel, err := provider.BroadcastChannelFor("test")
		if err != nil {
			t.Fatal(err)
		}

		broadcastChannel.SetUnmarshaler(func() net.TaggedUnmarshaler {
			return &coordinationMessage{}
		})
		// Register an unmarshaler for the signingDoneMessage that will
		// be uses to test the case with the wrong message type.
		broadcastChannel.SetUnmarshaler(func() net.TaggedUnmarshaler {
			return &signingDoneMessage{}
		})

		return struct{
			address chain.Address
			channel net.BroadcastChannel
		}{
			address: operatorAddress,
			channel: broadcastChannel,
		}
	}

	leader:= generateOperator()
	follower1 := generateOperator()
	follower2 := generateOperator()

	coordinatedWallet := wallet{
		// Set only relevant fields.
		publicKey: unmarshalPublicKey(publicKeyHex),
		signingGroupOperators: []chain.Address{
			follower1.address,
			follower2.address,
			leader.address,
			leader.address,
			follower2.address,
			follower1.address,
			follower1.address,
			follower2.address,
			leader.address,
			leader.address,
		},
	}

	leaderID := coordinatedWallet.membersByOperator(leader.address)[0]

	membershipValidator := group.NewMembershipValidator(
		&testutils.MockLogger{},
		coordinatedWallet.signingGroupOperators,
		Connect().Signing(),
	)

	// Set up the executor for follower 1.
	executor := &coordinationExecutor{
		// Set only relevant fields.
		coordinatedWallet:   coordinatedWallet,
		membersIndexes:      coordinatedWallet.membersByOperator(follower1.address),
		operatorAddress:     follower1.address,
		broadcastChannel:    follower1.channel,
		membershipValidator: membershipValidator,
	}

	ctx, cancelCtx := context.WithTimeout(context.Background(), 10 * time.Second)
	defer cancelCtx()

	go func() {
		// Give the follower routine some time to start and set up the
		// broadcast channel handler.
		time.Sleep(1 * time.Second)

		// Send message of wrong type.
		err := leader.channel.Send(ctx, &signingDoneMessage{
			senderID: leaderID,
			message: big.NewInt(100),
			attemptNumber: 2,
			signature: &tecdsa.Signature{
				R:          big.NewInt(200),
				S:          big.NewInt(300),
				RecoveryID: 3,
			},
			endBlock: 4500,
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send message from self.
		err = follower1.channel.Send(ctx, &coordinationMessage{
			senderID: coordinatedWallet.membersByOperator(follower1.address)[0],
			coordinationBlock: 900,
			walletPublicKeyHash: executor.walletPublicKeyHash(),
			proposal: &noopProposal{},
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send message with invalid membership.
		err = leader.channel.Send(ctx, &coordinationMessage{
			// Leader operator uses senderID controlled by follower 2.
			senderID: coordinatedWallet.membersByOperator(follower2.address)[0],
			coordinationBlock: 900,
			walletPublicKeyHash: executor.walletPublicKeyHash(),
			proposal: &noopProposal{},
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send message with wrong coordination block.
		err = leader.channel.Send(ctx, &coordinationMessage{
			// Proper block is 900.
			senderID: leaderID,
			coordinationBlock: 901,
			walletPublicKeyHash: executor.walletPublicKeyHash(),
			proposal: &noopProposal{},
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send message with wrong wallet.
		err = leader.channel.Send(ctx, &coordinationMessage{
			senderID: leaderID,
			coordinationBlock: 900,
			walletPublicKeyHash: [20]byte{0x01},
			proposal: &noopProposal{},
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send message that impersonates the leader.
		err = follower2.channel.Send(ctx, &coordinationMessage{
			senderID: coordinatedWallet.membersByOperator(follower2.address)[0],
			coordinationBlock: 900,
			walletPublicKeyHash: executor.walletPublicKeyHash(),
			proposal: &noopProposal{},
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send message with not allowed action proposal.
		err = leader.channel.Send(ctx, &coordinationMessage{
			// Heartbeat proposal is not allowed for this window.
			senderID: leaderID,
			coordinationBlock: 900,
			walletPublicKeyHash: executor.walletPublicKeyHash(),
			proposal: &HeartbeatProposal{
				Message: []byte("heartbeat message"),
			},
		})
		if err != nil {
			t.Error(err)
			return
		}

		// Send a proper message.
		err = leader.channel.Send(ctx, &coordinationMessage{
			senderID: leaderID,
			coordinationBlock: 900,
			walletPublicKeyHash: executor.walletPublicKeyHash(),
			proposal: &RedemptionProposal{
				RedeemersOutputScripts: []bitcoin.Script{
					parseScript("00148db50eb52063ea9d98b3eac91489a90f738986f6"),
					parseScript("76a9148db50eb52063ea9d98b3eac91489a90f738986f688ac"),
				},
				RedemptionTxFee: big.NewInt(10000),
			},
		})
		if err != nil {
			t.Error(err)
			return
		}
	}()

	proposal, err := executor.followerRoutine(
		ctx,
		leader.address,
		900,
		[]WalletActionType{ActionRedemption, ActionNoop},
	)
	if err != nil {
		t.Fatal(err)
	}

	expectedProposal := &RedemptionProposal{
		RedeemersOutputScripts: []bitcoin.Script{
			parseScript("00148db50eb52063ea9d98b3eac91489a90f738986f6"),
			parseScript("76a9148db50eb52063ea9d98b3eac91489a90f738986f688ac"),
		},
		RedemptionTxFee: big.NewInt(10000),
	}

	if !reflect.DeepEqual(expectedProposal, proposal) {
		t.Errorf(
			"unexpected proposal: \n"+
				"expected: %v\n"+
				"actual:   %v",
			expectedProposal,
			proposal,
		)
	}
}
