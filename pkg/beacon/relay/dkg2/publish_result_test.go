package dkg2

import (
	"math/big"
	"testing"

	relayChain "github.com/keep-network/keep-core/pkg/beacon/relay/chain"
	"github.com/keep-network/keep-core/pkg/chain"
	"github.com/keep-network/keep-core/pkg/chain/local"
)

func TestPublishDKGResult(t *testing.T) {
	threshold := 2
	groupSize := 5
	blockStep := 2 // T_step

	chainHandle, initialBlock, err := initChainHandle(threshold, groupSize)
	if err != nil {
		t.Fatal(err)
	}

	resultToPublish := &relayChain.DKGResult{
		GroupPublicKey: []byte{123, 45},
	}

	var tests = map[string]struct {
		publishingIndex int
		expectedTimeEnd int
	}{
		"first member eligible to publish straight away": {
			publishingIndex: 0,
			expectedTimeEnd: initialBlock, // T_now < T_init + T_step
		},
		"second member eligible to publish after T_step block passed": {
			publishingIndex: 1,
			expectedTimeEnd: initialBlock + blockStep, // T_now = T_init + T_step
		},
		"fourth member eligable to publish after T_dkg + 2*T_step passed": {
			publishingIndex: 3,
			expectedTimeEnd: initialBlock + 3*blockStep, // T_now = T_init + 3*T_step
		},
	}
	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			publisher := &Publisher{
				RequestID:       big.NewInt(101),
				publishingIndex: test.publishingIndex,
				chainHandle:     chainHandle,
				blockStep:       blockStep,
			}

			// Reinitialize chain to reset block counter
			publisher.chainHandle, initialBlock, err = initChainHandle(threshold, groupSize)
			if err != nil {
				t.Fatalf("chain initialization failed [%v]", err)
			}

			chainRelay := publisher.chainHandle.ThresholdRelay()

			isPublished, err := chainRelay.IsDKGResultPublished(publisher.RequestID)
			if err != nil {
				t.Fatal(err)
			}

			if isPublished {
				t.Fatalf("result is already published on chain")
			}
			// TEST
			currentBlock, err := publisher.publishResult(resultToPublish)
			if err != nil {
				t.Fatalf("\nexpected: %s\nactual:   %s\n", "", err)
			}
			if test.expectedTimeEnd != currentBlock {
				t.Fatalf(
					"invalid current block\nexpected: %v\nactual:   %v\n",
					test.expectedTimeEnd,
					currentBlock,
				)
			}
			isPublished, err = chainRelay.IsDKGResultPublished(publisher.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if !isPublished {
				t.Fatalf("result is not published on chain")
			}
		})
	}
}

// This tests runs result publication concurrently by two members.
// Member with lower index gets to publish the result to chain. For the second
// member loop should be aborted and result published by the first member should
// be returned.
func TestConcurrentPublishDKGResult(t *testing.T) {
	calculateExpectedBlockEnd := func(initialBlock, expectedDuration int) int {
		if expectedDuration >= 0 {
			return initialBlock + expectedDuration
		}
		return expectedDuration
	}

	threshold := 2
	groupSize := 5
	blockStep := 2 // t_step

	publisher1 := &Publisher{
		publishingIndex: 0, // P1
		blockStep:       blockStep,
	}
	publisher2 := &Publisher{
		publishingIndex: 3, // P4
		blockStep:       blockStep,
	}

	var tests = map[string]struct {
		resultToPublish1  *relayChain.DKGResult
		resultToPublish2  *relayChain.DKGResult
		requestID1        *big.Int
		requestID2        *big.Int
		expectedDuration1 int // index * t_step
		expectedDuration2 int // index * t_step
	}{
		"two members publish the same results": {
			resultToPublish1: &relayChain.DKGResult{
				GroupPublicKey: []byte{101},
			},
			resultToPublish2: &relayChain.DKGResult{
				GroupPublicKey: []byte{101},
			},
			requestID1:        big.NewInt(11),
			requestID2:        big.NewInt(11),
			expectedDuration1: 0,  // P1 * t_step
			expectedDuration2: -1, // result already published by member 1
		},
		"two members publish different results": {
			resultToPublish1: &relayChain.DKGResult{
				GroupPublicKey: []byte{201},
			},
			resultToPublish2: &relayChain.DKGResult{
				GroupPublicKey: []byte{202},
			},
			requestID1:        big.NewInt(11),
			requestID2:        big.NewInt(11),
			expectedDuration1: 0,  // P1 * t_step
			expectedDuration2: -1, // result already published by member 1
		},
		"two members publish the same results for different Request IDs": {
			resultToPublish1: &relayChain.DKGResult{
				GroupPublicKey: []byte{101},
			},
			resultToPublish2: &relayChain.DKGResult{
				GroupPublicKey: []byte{101},
			},
			requestID1:        big.NewInt(12),
			requestID2:        big.NewInt(13),
			expectedDuration1: 0,                                      // P1 * t_step
			expectedDuration2: publisher2.publishingIndex * blockStep, // P4 * t_step
		},
	}
	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			publisher1.RequestID = test.requestID1
			publisher2.RequestID = test.requestID2

			chainHandle, initialBlock, err := initChainHandle(threshold, groupSize)
			if err != nil {
				t.Fatal(err)
			}
			publisher1.chainHandle = chainHandle
			publisher2.chainHandle = chainHandle

			expectedBlockEnd1 := calculateExpectedBlockEnd(initialBlock, test.expectedDuration1)
			expectedBlockEnd2 := calculateExpectedBlockEnd(initialBlock, test.expectedDuration2)

			result1Chan := make(chan int)
			defer close(result1Chan)
			result2Chan := make(chan int)
			defer close(result2Chan)

			go func() {
				currentBlock, err := publisher1.publishResult(test.resultToPublish1)
				if err != nil {
					t.Fatal(err)
				}

				result1Chan <- currentBlock
			}()

			go func() {
				currentBlock, err := publisher2.publishResult(test.resultToPublish2)
				if err != nil {
					t.Fatal(err)
				}

				result2Chan <- currentBlock
			}()

			if result1 := <-result1Chan; result1 != expectedBlockEnd1 {
				t.Fatalf("\nexpected: %v\nactual:   %v\n", expectedBlockEnd1, result1)
			}
			if result2 := <-result2Chan; result2 != expectedBlockEnd2 {
				t.Fatalf("\nexpected: %v\nactual:   %v\n", expectedBlockEnd2, result2)
			}
		})
	}
}

func initChainHandle(threshold, groupSize int) (chainHandle chain.Handle, initialBlock int, err error) {
	chainHandle = local.Connect(groupSize, threshold)
	blockCounter, err := chainHandle.BlockCounter()
	if err != nil {
		return nil, -1, err
	}
	err = blockCounter.WaitForBlocks(1)
	if err != nil {
		return nil, -1, err
	}

	initialBlock, err = blockCounter.CurrentBlock()
	if err != nil {
		return nil, -1, err
	}
	return
}