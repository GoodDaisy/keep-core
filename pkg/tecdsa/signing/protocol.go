package signing

import (
	"context"
	"fmt"
	"github.com/bnb-chain/tss-lib/tss"
	"github.com/keep-network/keep-core/pkg/crypto/ephemeral"
	"github.com/keep-network/keep-core/pkg/protocol/group"
	"github.com/keep-network/keep-core/pkg/tecdsa/common"
)

// generateEphemeralKeyPair takes the group member list and generates an
// ephemeral ECDH keypair for every other group member. Generated public
// ephemeral keys are broadcasted within the group.
func (ekpgm *ephemeralKeyPairGeneratingMember) generateEphemeralKeyPair() (
	*ephemeralPublicKeyMessage,
	error,
) {
	ephemeralKeys := make(map[group.MemberIndex]*ephemeral.PublicKey)

	// Calculate ephemeral key pair for every other group member
	for _, member := range ekpgm.group.MemberIDs() {
		if member == ekpgm.id {
			// don’t actually generate a key with ourselves
			continue
		}

		ephemeralKeyPair, err := ephemeral.GenerateKeyPair()
		if err != nil {
			return nil, err
		}

		// save the generated ephemeral key to our state
		ekpgm.ephemeralKeyPairs[member] = ephemeralKeyPair

		// store the public key to the map for the message
		ephemeralKeys[member] = ephemeralKeyPair.PublicKey
	}

	return &ephemeralPublicKeyMessage{
		senderID:            ekpgm.id,
		ephemeralPublicKeys: ephemeralKeys,
		sessionID:           ekpgm.sessionID,
	}, nil
}

// generateSymmetricKeys attempts to generate symmetric keys for all remote group
// members via ECDH. It generates this symmetric key for each remote group member
// by doing an ECDH between the ephemeral private key generated for a remote
// group member, and the public key for this member, generated and broadcasted by
// the remote group member.
func (skgm *symmetricKeyGeneratingMember) generateSymmetricKeys(
	ephemeralPubKeyMessages []*ephemeralPublicKeyMessage,
) error {
	for _, ephemeralPubKeyMessage := range deduplicateBySender(ephemeralPubKeyMessages) {
		otherMember := ephemeralPubKeyMessage.senderID

		if !skgm.isValidEphemeralPublicKeyMessage(ephemeralPubKeyMessage) {
			return fmt.Errorf(
				"member [%v] sent invalid ephemeral public key message",
				otherMember,
			)
		}

		// Find the ephemeral key pair generated by this group member for
		// the other group member.
		ephemeralKeyPair, ok := skgm.ephemeralKeyPairs[otherMember]
		if !ok {
			return fmt.Errorf(
				"ephemeral key pair does not exist for member [%v]",
				otherMember,
			)
		}

		// Get the ephemeral private key generated by this group member for
		// the other group member.
		thisMemberEphemeralPrivateKey := ephemeralKeyPair.PrivateKey

		// Get the ephemeral public key broadcasted by the other group member,
		// which was intended for this group member.
		otherMemberEphemeralPublicKey :=
			ephemeralPubKeyMessage.ephemeralPublicKeys[skgm.id]

		// Create symmetric key for the current group member and the other
		// group member by ECDH'ing the public and private key.
		symmetricKey := thisMemberEphemeralPrivateKey.Ecdh(
			otherMemberEphemeralPublicKey,
		)
		skgm.symmetricKeys[otherMember] = symmetricKey
	}

	return nil
}

// isValidEphemeralPublicKeyMessage validates a given EphemeralPublicKeyMessage.
// Message is considered valid if it contains ephemeral public keys for
// all other group members.
func (skgm *symmetricKeyGeneratingMember) isValidEphemeralPublicKeyMessage(
	message *ephemeralPublicKeyMessage,
) bool {
	for _, memberID := range skgm.group.MemberIDs() {
		if memberID == message.senderID {
			// Message contains ephemeral public keys only for other group members
			continue
		}

		if _, ok := message.ephemeralPublicKeys[memberID]; !ok {
			skgm.logger.Warningf(
				"[member:%v] ephemeral public key message from member [%v] "+
					"does not contain public key for member [%v]",
				skgm.id,
				message.senderID,
				memberID,
			)
			return false
		}
	}

	return true
}

// tssRoundOne starts the TSS process by executing its first round. The
// outcome of that round is a message containing TSS round one components.
func (trom *tssRoundOneMember) tssRoundOne(
	ctx context.Context,
) (*tssRoundOneMessage, error) {
	if err := trom.tssParty.Start(); err != nil {
		return nil, fmt.Errorf(
			"failed to start TSS round one: [%v]",
			err,
		)
	}

	// Listen for TSS outgoing messages. We expect N-1 P2P messages (where N
	// is the number of properly operating members) and 1 broadcast message.
	var tssMessages []tss.Message
outgoingMessagesLoop:
	for {
		select {
		case tssMessage := <-trom.tssOutgoingMessagesChan:
			tssMessages = append(tssMessages, tssMessage)

			if len(tssMessages) == len(trom.group.OperatingMemberIDs()) {
				break outgoingMessagesLoop
			}
		case <-ctx.Done():
			return nil, fmt.Errorf(
				"TSS round one outgoing messages were not " +
					"generated on time",
			)
		}
	}

	broadcastPayload, peersPayload, err := common.AggregateTssMessages(
		tssMessages,
		trom.symmetricKeys,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"cannot aggregate TSS round one outgoing messages: [%w]",
			err,
		)
	}

	ok := len(broadcastPayload) > 0 &&
		len(peersPayload) == len(trom.group.OperatingMemberIDs())-1
	if !ok {
		return nil, fmt.Errorf("cannot produce a proper TSS round one message")
	}

	return &tssRoundOneMessage{
		senderID:         trom.id,
		broadcastPayload: broadcastPayload,
		peersPayload:     peersPayload,
		sessionID:        trom.sessionID,
	}, nil
}

// tssRoundTwo performs the second round of the TSS process. The outcome of
// that round is a message containing TSS round two components.
func (trtm *tssRoundTwoMember) tssRoundTwo(
	ctx context.Context,
	tssRoundOneMessages []*tssRoundOneMessage,
) (*tssRoundTwoMessage, error) {
	// Use messages from round one to update the local party and advance
	// to round two.
	for _, tssRoundOneMessage := range deduplicateBySender(tssRoundOneMessages) {
		senderID := tssRoundOneMessage.SenderID()
		senderTssPartyID := common.ResolveSortedTssPartyID(trtm.tssParameters, senderID)

		// Update the local TSS party using the broadcast part of the message
		// produced in round one.
		_, tssErr := trtm.tssParty.UpdateFromBytes(
			tssRoundOneMessage.broadcastPayload,
			senderTssPartyID,
			true,
		)
		if tssErr != nil {
			return nil, fmt.Errorf(
				"cannot update using the broadcast part of the "+
					"TSS round one message from member [%v]: [%v]",
				senderID,
				tssErr,
			)
		}

		// Check if the sender produced a P2P part of the TSS round one message
		// for this member.
		encryptedPeerPayload, ok := tssRoundOneMessage.peersPayload[trtm.id]
		if !ok {
			return nil, fmt.Errorf(
				"no P2P part in the TSS round one message from member [%v]",
				senderID,
			)
		}
		// Get the symmetric key with the sender. If the symmetric key
		// cannot be found, something awful happened.
		symmetricKey, ok := trtm.symmetricKeys[senderID]
		if !ok {
			return nil, fmt.Errorf(
				"cannot get symmetric key with member [%v]",
				senderID,
			)
		}
		// Decrypt the P2P part of the TSS round one message.
		peerPayload, err := symmetricKey.Decrypt(encryptedPeerPayload)
		if err != nil {
			return nil, fmt.Errorf(
				"cannot decrypt P2P part of the TSS round one "+
					"message from member [%v]: [%v]",
				senderID,
				err,
			)
		}
		// Update the local TSS party using the P2P part of the message
		// produced in round one.
		_, tssErr = trtm.tssParty.UpdateFromBytes(
			peerPayload,
			senderTssPartyID,
			false,
		)
		if tssErr != nil {
			return nil, fmt.Errorf(
				"cannot update using the P2P part of the TSS round "+
					"one message from member [%v]: [%v]",
				senderID,
				tssErr,
			)
		}
	}

	// Listen for TSS outgoing messages. We expect N-1 P2P messages (where N
	// is the number of properly operating members) and 0 broadcast messages.
	var tssMessages []tss.Message
outgoingMessagesLoop:
	for {
		select {
		case tssMessage := <-trtm.tssOutgoingMessagesChan:
			tssMessages = append(tssMessages, tssMessage)

			if len(tssMessages) == len(trtm.group.OperatingMemberIDs())-1 {
				break outgoingMessagesLoop
			}
		case <-ctx.Done():
			return nil, fmt.Errorf(
				"TSS round two outgoing messages were not " +
					"generated on time",
			)
		}
	}

	broadcastPayload, peersPayload, err := common.AggregateTssMessages(
		tssMessages,
		trtm.symmetricKeys,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"cannot aggregate TSS round two outgoing messages: [%w]",
			err,
		)
	}

	// Unlike the previous phase (TSS round 1), we don't expect a broadcast
	// payload here.
	ok := len(broadcastPayload) == 0 &&
		len(peersPayload) == len(trtm.group.OperatingMemberIDs())-1
	if !ok {
		return nil, fmt.Errorf("cannot produce a proper TSS round two message")
	}

	return &tssRoundTwoMessage{
		senderID:         trtm.id,
		peersPayload:     peersPayload,
		sessionID:        trtm.sessionID,
	}, nil
}

// tssRoundThree performs the third round of the TSS process. The outcome of
// that round is a message containing TSS round three components.
func (trtm *tssRoundThreeMember) tssRoundThree(
	ctx context.Context,
	tssRoundTwoMessages []*tssRoundTwoMessage,
) (*tssRoundThreeMessage, error) {
	// Use messages from round two to update the local party and advance
	// to round three.
	for _, tssRoundTwoMessage := range deduplicateBySender(tssRoundTwoMessages) {
		senderID := tssRoundTwoMessage.SenderID()
		senderTssPartyID := common.ResolveSortedTssPartyID(trtm.tssParameters, senderID)

		// Check if the sender produced a P2P part of the TSS round two message
		// for this member.
		encryptedPeerPayload, ok := tssRoundTwoMessage.peersPayload[trtm.id]
		if !ok {
			return nil, fmt.Errorf(
				"no P2P part in the TSS round two message from member [%v]",
				senderID,
			)
		}
		// Get the symmetric key with the sender. If the symmetric key
		// cannot be found, something awful happened.
		symmetricKey, ok := trtm.symmetricKeys[senderID]
		if !ok {
			return nil, fmt.Errorf(
				"cannot get symmetric key with member [%v]",
				senderID,
			)
		}
		// Decrypt the P2P part of the TSS round two message.
		peerPayload, err := symmetricKey.Decrypt(encryptedPeerPayload)
		if err != nil {
			return nil, fmt.Errorf(
				"cannot decrypt P2P part of the TSS round two "+
					"message from member [%v]: [%v]",
				senderID,
				err,
			)
		}
		// Update the local TSS party using the P2P part of the message
		// produced in round two.
		_, tssErr := trtm.tssParty.UpdateFromBytes(
			peerPayload,
			senderTssPartyID,
			false,
		)
		if tssErr != nil {
			return nil, fmt.Errorf(
				"cannot update using the P2P part of the TSS round "+
					"two message from member [%v]: [%v]",
				senderID,
				tssErr,
			)
		}
	}

	// We expect exactly one TSS message to be produced in this phase.
	select {
	case tssMessage := <-trtm.tssOutgoingMessagesChan:
		tssMessageBytes, _, err := tssMessage.WireBytes()
		if err != nil {
			return nil, fmt.Errorf(
				"failed to encode TSS round three message: [%v]",
				err,
			)
		}

		return &tssRoundThreeMessage{
			senderID:  trtm.id,
			payload:   tssMessageBytes,
			sessionID: trtm.sessionID,
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf(
			"TSS round three outgoing message was not generated on time",
		)
	}
}

// tssRoundFour performs the fourth round of the TSS process. The outcome of
// that round is a message containing TSS round four components.
func (trfm *tssRoundFourMember) tssRoundFour(
	ctx context.Context,
	tssRoundThreeMessages []*tssRoundThreeMessage,
) (*tssRoundFourMessage, error) {
	// Use messages from round three to update the local party and advance
	// to round four.
	for _, tssRoundThreeMessage := range deduplicateBySender(tssRoundThreeMessages) {
		senderID := tssRoundThreeMessage.SenderID()

		_, tssErr := trfm.tssParty.UpdateFromBytes(
			tssRoundThreeMessage.payload,
			common.ResolveSortedTssPartyID(trfm.tssParameters, senderID),
			true,
		)
		if tssErr != nil {
			return nil, fmt.Errorf(
				"cannot update using TSS round three message "+
					"from member [%v]: [%v]",
				senderID,
				tssErr,
			)
		}
	}

	// We expect exactly one TSS message to be produced in this phase.
	select {
	case tssMessage := <-trfm.tssOutgoingMessagesChan:
		tssMessageBytes, _, err := tssMessage.WireBytes()
		if err != nil {
			return nil, fmt.Errorf(
				"failed to encode TSS round four message: [%v]",
				err,
			)
		}

		return &tssRoundFourMessage{
			senderID:  trfm.id,
			payload:   tssMessageBytes,
			sessionID: trfm.sessionID,
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf(
			"TSS round four outgoing message was not generated on time",
		)
	}
}

// tssRoundFive performs the fifth round of the TSS process. The outcome of
// that round is a message containing TSS round five components.
func (trfm *tssRoundFiveMember) tssRoundFive(
	ctx context.Context,
	tssRoundFourMessages []*tssRoundFourMessage,
) (*tssRoundFiveMessage, error) {
	// Use messages from round four to update the local party and advance
	// to round five.
	for _, tssRoundFourMessage := range deduplicateBySender(tssRoundFourMessages) {
		senderID := tssRoundFourMessage.SenderID()

		_, tssErr := trfm.tssParty.UpdateFromBytes(
			tssRoundFourMessage.payload,
			common.ResolveSortedTssPartyID(trfm.tssParameters, senderID),
			true,
		)
		if tssErr != nil {
			return nil, fmt.Errorf(
				"cannot update using TSS round four message "+
					"from member [%v]: [%v]",
				senderID,
				tssErr,
			)
		}
	}

	// We expect exactly one TSS message to be produced in this phase.
	select {
	case tssMessage := <-trfm.tssOutgoingMessagesChan:
		tssMessageBytes, _, err := tssMessage.WireBytes()
		if err != nil {
			return nil, fmt.Errorf(
				"failed to encode TSS round five message: [%v]",
				err,
			)
		}

		return &tssRoundFiveMessage{
			senderID:  trfm.id,
			payload:   tssMessageBytes,
			sessionID: trfm.sessionID,
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf(
			"TSS round five outgoing message was not generated on time",
		)
	}
}

// tssRoundSix performs the sixth round of the TSS process. The outcome of
// that round is a message containing TSS round six components.
func (trsm *tssRoundSixMember) tssRoundSix(
	ctx context.Context,
	tssRoundFiveMessages []*tssRoundFiveMessage,
) (*tssRoundSixMessage, error) {
	// Use messages from round five to update the local party and advance
	// to round six.
	for _, tssRoundFiveMessage := range deduplicateBySender(tssRoundFiveMessages) {
		senderID := tssRoundFiveMessage.SenderID()

		_, tssErr := trsm.tssParty.UpdateFromBytes(
			tssRoundFiveMessage.payload,
			common.ResolveSortedTssPartyID(trsm.tssParameters, senderID),
			true,
		)
		if tssErr != nil {
			return nil, fmt.Errorf(
				"cannot update using TSS round five message "+
					"from member [%v]: [%v]",
				senderID,
				tssErr,
			)
		}
	}

	// We expect exactly one TSS message to be produced in this phase.
	select {
	case tssMessage := <-trsm.tssOutgoingMessagesChan:
		tssMessageBytes, _, err := tssMessage.WireBytes()
		if err != nil {
			return nil, fmt.Errorf(
				"failed to encode TSS round six message: [%v]",
				err,
			)
		}

		return &tssRoundSixMessage{
			senderID:  trsm.id,
			payload:   tssMessageBytes,
			sessionID: trsm.sessionID,
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf(
			"TSS round six outgoing message was not generated on time",
		)
	}
}

// deduplicateBySender removes duplicated items for the given sender.
// It always takes the first item that occurs for the given sender
// and ignores the subsequent ones.
func deduplicateBySender[T interface{ SenderID() group.MemberIndex }](
	list []T,
) []T {
	senders := make(map[group.MemberIndex]bool)
	result := make([]T, 0)

	for _, item := range list {
		if _, exists := senders[item.SenderID()]; !exists {
			senders[item.SenderID()] = true
			result = append(result, item)
		}
	}

	return result
}