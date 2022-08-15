package tbtc

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/keep-network/keep-core/pkg/chain"
	"github.com/keep-network/keep-core/pkg/tecdsa/retry"
	"math/big"
	"time"

	"github.com/keep-network/keep-common/pkg/persistence"
	"github.com/keep-network/keep-core/pkg/internal/testutils"
	"github.com/keep-network/keep-core/pkg/net"
	"github.com/keep-network/keep-core/pkg/protocol/group"
	"github.com/keep-network/keep-core/pkg/tecdsa/dkg"
)

// TODO: Unit tests for `node.go`.

// node represents the current state of an ECDSA node.
type node struct {
	chain          Chain
	netProvider    net.Provider
	walletRegistry *walletRegistry
	dkgExecutor    *dkg.Executor
}

func newNode(
	chain Chain,
	netProvider net.Provider,
	persistence persistence.Handle,
) *node {
	walletRegistry := newWalletRegistry(persistence)

	// TODO: Pass TSS pre-parameters pool config from the outside.
	dkgExecutor := dkg.NewExecutor(logger, &dkg.ExecutorConfig{
		TssPreParamsPoolSize:              50,
		TssPreParamsPoolGenerationTimeout: 2 * time.Minute,
	})

	return &node{
		chain:          chain,
		netProvider:    netProvider,
		walletRegistry: walletRegistry,
		dkgExecutor:    dkgExecutor,
	}
}

// joinDKGIfEligible takes a seed value and undergoes the process of the
// distributed key generation if this node's operator proves to be eligible for
// the group generated by that seed. This is an interactive on-chain process,
// and joinDKGIfEligible can block for an extended period of time while it
// completes the on-chain operation.
func (n *node) joinDKGIfEligible(seed *big.Int, startBlockNumber uint64) {
	logger.Infof(
		"checking eligibility for DKG with seed [0x%x]",
		seed,
	)

	selectedSigningGroupOperators, err := n.chain.SelectGroup(seed)
	if err != nil {
		logger.Errorf(
			"failed to select group with seed [0x%x]: [%v]",
			seed,
			err,
		)
		return
	}

	chainConfig := n.chain.GetConfig()

	if len(selectedSigningGroupOperators) > chainConfig.GroupSize {
		logger.Errorf(
			"group size larger than supported: [%v]",
			len(selectedSigningGroupOperators),
		)
		return
	}

	signing := n.chain.Signing()

	_, operatorPublicKey, err := n.chain.OperatorKeyPair()
	if err != nil {
		logger.Errorf("failed to get operator public key: [%v]", err)
		return
	}

	operatorAddress, err := signing.PublicKeyToAddress(operatorPublicKey)
	if err != nil {
		logger.Errorf("failed to get operator address: [%v]", err)
		return
	}

	indexes := make([]uint8, 0)
	for index, operator := range selectedSigningGroupOperators {
		// See if we are amongst those chosen
		if operator == operatorAddress {
			indexes = append(indexes, uint8(index))
		}
	}

	// Create temporary broadcast channel name for DKG using the
	// group selection seed with the protocol name as prefix.
	channelName := fmt.Sprintf("%s-%s", ProtocolName, seed.Text(16))

	if len(indexes) > 0 {
		logger.Infof(
			"joining DKG with seed [0x%x] and controlling [%v] group members",
			seed,
			len(indexes),
		)

		broadcastChannel, err := n.netProvider.BroadcastChannelFor(channelName)
		if err != nil {
			logger.Errorf("failed to get broadcast channel: [%v]", err)
			return
		}

		membershipValidator := group.NewMembershipValidator(
			&testutils.MockLogger{},
			selectedSigningGroupOperators,
			signing,
		)

		err = broadcastChannel.SetFilter(membershipValidator.IsInGroup)
		if err != nil {
			logger.Errorf(
				"could not set filter for channel [%v]: [%v]",
				broadcastChannel.Name(),
				err,
			)
		}

		blockCounter, err := n.chain.BlockCounter()
		if err != nil {
			logger.Errorf("failed to get block counter: [%v]", err)
			return
		}

		for _, index := range indexes {
			// Capture the member index for the goroutine. The group member
			// index should be in range [1, groupSize] so we need to add 1.
			memberIndex := index + 1

			go func() {
				retryLoop := newDkgRetryLoop(
					seed,
					startBlockNumber,
					memberIndex,
					selectedSigningGroupOperators,
					chainConfig,
				)

				result, err := retryLoop.start(
					func(attempt *dkgAttemptParams) (*dkg.Result, error) {
						logger.Infof(
							"[member:%v] starting dkg attempt [%v] "+
								"with [%v] group members (excluded: [%v])",
							memberIndex,
							attempt.index,
							chainConfig.GroupSize-len(attempt.excludedMembers),
							attempt.excludedMembers,
						)

						// sessionID must be different for each attempt.
						sessionID := fmt.Sprintf(
							"%v-%v",
							seed.Text(16),
							attempt.index,
						)

						result, _, err := n.dkgExecutor.Execute(
							sessionID,
							attempt.startBlock,
							memberIndex,
							chainConfig.GroupSize,
							chainConfig.DishonestThreshold(),
							attempt.excludedMembers,
							blockCounter,
							broadcastChannel,
							membershipValidator,
						)
						if err != nil {
							logger.Errorf(
								"[member:%v] dkg attempt [%v] "+
									"failed: [%v]",
								memberIndex,
								attempt.index,
								err,
							)

							return nil, err
						}

						return result, nil
					},
				)
				if err != nil {
					logger.Errorf(
						"[member:%v] failed to execute dkg: [%v]",
						memberIndex,
						err,
					)
					return
				}

				// TODO: Snapshot the key material before doing on-chain result
				//       submission.

				// TODO: Submit the result using the chain layer.

				// TODO: The final `signingGroupOperators` may differ from
				//       the original `selectedSigningGroupOperators`.
				//       Consider that when integrating the retry algorithm.
				signer := newSigner(
					result.PrivateKeyShare.PublicKey(),
					selectedSigningGroupOperators,
					memberIndex,
					result.PrivateKeyShare,
				)

				err = n.walletRegistry.registerSigner(signer)
				if err != nil {
					logger.Errorf(
						"failed to register %s: [%v]",
						signer,
						err,
					)
					return
				}

				logger.Infof("registered %s", signer)
			}()
		}
	} else {
		logger.Infof("not eligible for DKG with seed [0x%x]", seed)
	}
}

// dkgRetryLoop is a struct that encapsulates the DKG retry logic.
type dkgRetryLoop struct {
	initialStartBlock    uint64
	memberIndex          group.MemberIndex
	selectedOperators    chain.Addresses
	inactiveOperatorsSet map[chain.Address]bool
	chainConfig          *ChainConfig
	attemptCounter       uint
	randomRetryCounter   uint
	randomRetrySeed      int64
}

func newDkgRetryLoop(
	seed *big.Int,
	initialStartBlock uint64,
	memberIndex group.MemberIndex,
	selectedOperators chain.Addresses,
	chainConfig *ChainConfig,
) *dkgRetryLoop {
	// Pre-compute the 8-byte seed that may be needed for the random
	// retry algorithm. Since the original DKG seed passed as parameter
	// can have a variable length, it is safer to take the first 8 bytes
	// of sha256(seed) as the randomRetrySeed.
	seedSha256 := sha256.Sum256(seed.Bytes())
	randomRetrySeed := int64(binary.BigEndian.Uint64(seedSha256[:8]))

	return &dkgRetryLoop{
		initialStartBlock:    initialStartBlock,
		memberIndex:          memberIndex,
		selectedOperators:    selectedOperators,
		inactiveOperatorsSet: make(map[chain.Address]bool),
		chainConfig:          chainConfig,
		attemptCounter:       0,
		randomRetryCounter:   0,
		randomRetrySeed:      randomRetrySeed,
	}
}

// dkgAttemptParams represents parameters of a DKG attempt.
type dkgAttemptParams struct {
	index           uint
	startBlock      uint64
	excludedMembers []group.MemberIndex
}

// dkgAttemptFn represents a function performing a DKG attempt.
type dkgAttemptFn func(*dkgAttemptParams) (*dkg.Result, error)

// start begins the DKG retry loop using the given DKG attempt function.
func (drl *dkgRetryLoop) start(dkgAttemptFn dkgAttemptFn) (*dkg.Result, error) {
	// All selected operators should be qualified for the first attempt.
	qualifiedOperatorsSet := drl.selectedOperators.Set()

	// TODO: Other stop conditions for that loop (e.g result submitted on-chain).
	for {
		drl.attemptCounter++

		// Exclude all members controlled by the operators that were not
		// qualified for the current attempt.
		excludedMembers := make([]group.MemberIndex, 0)
		for i, operator := range drl.selectedOperators {
			if !qualifiedOperatorsSet[operator] {
				excludedMembers = append(excludedMembers, group.MemberIndex(i+1))
			}
		}

		// In order to start the given attempt in the right place, we need to
		// determine how many blocks were taken by previous attempts. We assume
		// the worst case that each attempt failed at the end of the DKG
		// protocol. That said, we need to shift by the multiplication of the
		// previous attempts count and the duration of a single attempt.
		blocksShift := uint64(drl.attemptCounter-1) * dkg.ProtocolBlocks()
		// We also need to add a small fixed delay in order to mitigate all
		// corner cases where the actual attempt duration was slightly longer
		// than the expected duration determined by the dkg.ProtocolBlocks
		// function. For example, the attempt may fail at the end of the
		// protocol but the error is returned after some time and more
		// blocks than expected are mined in the meantime. Additionally,
		// we want to strongly extend the delay period periodically
		// in order to give some additional time for nodes to recover and
		// re-fill their internal TSS pre-parameters pools.
		delayBlocks := uint64(5)
		if drl.attemptCounter%100 == 0 {
			delayBlocks = 100
		}

		// TODO: What if the executing member is among the excluded members?
		result, err := dkgAttemptFn(&dkgAttemptParams{
			index:           drl.attemptCounter,
			startBlock:      drl.initialStartBlock + blocksShift + delayBlocks,
			excludedMembers: excludedMembers,
		})
		if err != nil {
			var imErr *dkg.InactiveMembersError
			if errors.As(err, &imErr) {
				for _, memberIndex := range imErr.InactiveMembersIndexes {
					operator := drl.selectedOperators[memberIndex-1]
					drl.inactiveOperatorsSet[operator] = true
				}
			}

			qualifiedOperatorsSet, err = drl.qualifiedOperatorsSet()
			if err != nil {
				return nil, fmt.Errorf(
					"cannot recover after failed dkg attempt [%v]: [%w]",
					drl.attemptCounter,
					err,
				)
			}

			continue
		}

		return result, nil
	}
}

// qualifiedOperatorsSet returns a set of operators qualified to participate
// in the given DKG attempt.
func (drl *dkgRetryLoop) qualifiedOperatorsSet() (map[chain.Address]bool, error) {
	// If this is one of the first attempts and random retries were not started
	// yet, check if there are known inactive operators. If the group quorum
	// can be maintained, just exclude the members controlled by the inactive
	// operators from the qualified set.
	if drl.attemptCounter <= 5 &&
		drl.randomRetryCounter == 0 &&
		len(drl.inactiveOperatorsSet) > 0 {
		qualifiedOperators := make(chain.Addresses, 0)
		for _, operator := range drl.selectedOperators {
			if !drl.inactiveOperatorsSet[operator] {
				qualifiedOperators = append(qualifiedOperators, operator)
			}
		}

		if len(qualifiedOperators) >= drl.chainConfig.GroupQuorum {
			return qualifiedOperators.Set(), nil
		}
	}

	// In any other case, try to make a random retry.
	qualifiedOperators, err := retry.EvaluateRetryParticipantsForKeyGeneration(
		drl.selectedOperators,
		drl.randomRetrySeed,
		drl.randomRetryCounter,
		uint(drl.chainConfig.GroupQuorum),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"random operator selection failed: [%w]",
			err,
		)
	}

	drl.randomRetryCounter++
	return chain.Addresses(qualifiedOperators).Set(), nil
}
