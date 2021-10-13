// SPDX-License-Identifier: MIT

pragma solidity ^0.8.6;

import "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import "../utils/BytesLib.sol";

library DKG {
  using BytesLib for bytes;
  using ECDSA for bytes32;

  struct Data {
    // Time in blocks after which DKG result is complete and ready to be
    // published by clients.
    uint256 timeDKG;
    // Time in blocks after which the next group member is eligible
    // to submit DKG result.
    uint256 resultPublicationBlockStep;
    // Size of a group in the threshold relay.
    uint256 groupSize;
    // The minimum number of signatures required to support DKG result.
    // This number needs to be at least the same as the signing threshold
    // and it is recommended to make it higher than the signing threshold
    // to keep a safety margin for misbehaving members.
    uint256 signatureThreshold;
    // Time in blocks at which DKG started.
    uint256 startBlock;
    // Mapping of submitted DKG result hash with submission block number.
    // This map is not cleaned after each DKG completion, it can hold entires
    // from past executions. The results should be filtered based on the current
    // execution's startBlock.
    mapping(bytes32 => uint256) registeredDkgResults;
  }

  /// @notice DKG result.
  struct DkgResult {
    // Claimed submitter candidate group member index
    uint256 submitterMemberIndex;
    // Generated candidate group public key
    bytes groupPubKey;
    // Bytes array of misbehaved (disqualified or inactive)
    bytes misbehaved;
    // Concatenation of signatures from members supporting the result.
    bytes signatures;
    // Indices of members corresponding to each signature. Indices have to be unique.
    uint256[] signingMemberIndices;
    // Addresses of candidate group members as outputted by the group selection protocol.
    address[] members;
  }

  function start(Data storage self, uint256 resultPublicationBlockStep)
    internal
  {
    assert(self.groupSize > 0);
    assert(self.signatureThreshold > 0);
    assert(self.timeDKG > 0);

    require(!isInProgress(self), "dkg is currently in progress");

    require(
      resultPublicationBlockStep > 0,
      "resultPublicationBlockStep not set"
    );

    self.resultPublicationBlockStep = resultPublicationBlockStep;

    self.startBlock = block.number;
  }

  function submitDkgResult(Data storage self, DkgResult calldata dkgResult)
    external
  {
    require(isInProgress(self), "dkg is currently not in progress");

    bytes32 dkgResultHash = keccak256(abi.encode(dkgResult));

    require(
      self.registeredDkgResults[dkgResultHash] < self.startBlock,
      "this dkg result was already submitted in the current dkg"
    );

    assert(self.startBlock > 0);
    assert(self.resultPublicationBlockStep > 0);

    verify(
      self,
      dkgResult.submitterMemberIndex,
      dkgResult.groupPubKey,
      dkgResult.misbehaved,
      dkgResult.signatures,
      dkgResult.signingMemberIndices,
      dkgResult.members,
      self.startBlock
    );

    self.registeredDkgResults[dkgResultHash] = block.number;
  }

  /// @notice Checks if DKG is currently in progress.
  /// @return True if DKG is in progress, false otherwise.
  function isInProgress(Data storage self) public view returns (bool) {
    return self.startBlock > 0;
  }

  /// @notice Verifies the submitted DKG result against supporting member
  /// signatures and if the submitter is eligible to submit at the current
  /// block. Every signature supporting the result has to be from a unique
  /// group member.
  ///
  /// @param submitterMemberIndex Claimed submitter candidate group member index
  /// @param groupPubKey Generated candidate group public key
  /// @param misbehaved Bytes array of misbehaved (disqualified or inactive)
  /// group members indexes; Indexes reflect positions of members in the group,
  /// as outputted by the group selection protocol.
  /// @param signatures Concatenation of signatures from members supporting the
  /// result.
  /// @param signingMemberIndices Indices of members corresponding to each
  /// signature. Indices have to be unique.
  /// @param members Addresses of candidate group members as outputted by the
  /// group selection protocol.
  /// @param groupSelectionEndBlock Block height at which the group selection
  /// protocol ended.
  function verify(
    Data storage self,
    uint256 submitterMemberIndex,
    bytes memory groupPubKey,
    bytes memory misbehaved,
    bytes memory signatures,
    uint256[] memory signingMemberIndices,
    address[] memory members,
    uint256 groupSelectionEndBlock
  ) public view {
    require(submitterMemberIndex > 0, "Invalid submitter index");
    require(
      members[submitterMemberIndex - 1] == msg.sender,
      "Unexpected submitter index"
    );

    uint256 T_init = groupSelectionEndBlock + self.timeDKG;
    require(
      block.number >=
        (T_init + (submitterMemberIndex - 1) * self.resultPublicationBlockStep),
      "Submitter not eligible"
    );

    require(groupPubKey.length == 128, "Malformed group public key");

    require(
      misbehaved.length <= self.groupSize - self.signatureThreshold,
      "Malformed misbehaved bytes"
    );

    uint256 signaturesCount = signatures.length / 65;
    require(signatures.length >= 65, "Too short signatures array");
    require(signatures.length % 65 == 0, "Malformed signatures array");
    require(
      signaturesCount == signingMemberIndices.length,
      "Unexpected signatures count"
    );
    require(signaturesCount >= self.signatureThreshold, "Too few signatures");
    require(signaturesCount <= self.groupSize, "Too many signatures");

    bytes32 resultHash = keccak256(abi.encodePacked(groupPubKey, misbehaved));

    bytes memory current; // Current signature to be checked.

    bool[] memory usedMemberIndices = new bool[](self.groupSize);

    for (uint256 i = 0; i < signaturesCount; i++) {
      uint256 memberIndex = signingMemberIndices[i];
      require(memberIndex > 0, "Invalid index");
      require(memberIndex <= members.length, "Index out of range");

      require(!usedMemberIndices[memberIndex - 1], "Duplicate member index");
      usedMemberIndices[memberIndex - 1] = true;

      current = signatures.slice(65 * i, 65);
      address recoveredAddress = resultHash.toEthSignedMessageHash().recover(
        current
      );
      require(
        members[memberIndex - 1] == recoveredAddress,
        "Invalid signature"
      );
    }
  }
}
