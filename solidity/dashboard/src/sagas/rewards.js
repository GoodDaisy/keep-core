import { take, takeLatest, call, put, fork } from "redux-saga/effects"
import { logError, submitButtonHelper, getContractsContext } from "./utils"
import {
  fetchtTotalDistributedRewards,
  fetchECDSAAvailableRewards,
  fetchECDSAClaimedRewards,
} from "../services/rewards"
import { sendTransaction } from "./web3"
import { isSameEthAddress } from "../utils/general.utils"
import { add } from "../utils/arithmetics.utils"
import { getOperatorsOfBeneficiary } from "../services/token-staking.service"
import { REWARD_STATUS } from "../constants/constants"

function* fetchBeaconDistributedRewards(address) {
  try {
    yield put({ type: "rewards/beacon_fetch_distributed_rewards_start" })
    const balance = yield call(
      fetchtTotalDistributedRewards,
      address,
      "beaconRewardsContract"
    )
    yield put({
      type: "rewards/beacon_fetch_distributed_rewards_success",
      payload: balance,
    })
  } catch (error) {
    yield* logError("rewards/beacon_fetch_distributed_rewards_failure", error)
  }
}

export function* watchFetchBeaconDistributedRewards() {
  const { payload } = yield take(
    "rewards/beacon_fetch_distributed_rewards_request"
  )
  yield fork(fetchBeaconDistributedRewards, payload)
}

function* withdrawECDSARewards(action) {
  const { payload: availableRewards } = action
  const { ECDSARewardsDistributorContract } = yield getContractsContext()

  for (const {
    merkleRoot,
    index,
    operator,
    amount,
    proof,
  } of availableRewards) {
    try {
      yield call(sendTransaction, {
        payload: {
          contract: ECDSARewardsDistributorContract,
          methodName: "claim",
          args: [merkleRoot, index, operator, amount, proof],
        },
      })
    } catch (error) {
      continue
    }
  }
}

export function* watchWithdrawECDSARewards() {
  yield takeLatest("rewards/ecdsa_withdraw", function* (action) {
    yield call(submitButtonHelper, withdrawECDSARewards, action)
  })
}

function* fetchECDSARewardsData(action) {
  const beneficiary = action.payload
  try {
    yield put({ type: "rewards/ecdsa_fetch_rewards_data_start" })

    const operators = yield call(getOperatorsOfBeneficiary, beneficiary)
    let availableRewards = yield call(fetchECDSAAvailableRewards, operators)
    const claimedRewards = yield call(fetchECDSAClaimedRewards, operators)

    // Available rewards are fetched from merkle generator's output file. This
    // file doesn't take into account a rewards alredy claimed. So we need to
    // filter out claimed rewards.
    availableRewards = availableRewards.filter(
      ({ operator, merkleRoot }) =>
        !claimedRewards.find(
          (lookup) =>
            isSameEthAddress(operator, lookup.operator) &&
            merkleRoot === lookup.merkleRoot
        )
    )

    const totalAvailableAmount = availableRewards.reduce(
      (reducer, _) => add(reducer, _.amount),
      0
    )

    const rewardsHistory = availableRewards
      .map((reward) => ({
        ...reward,
        status: REWARD_STATUS.AVAILABLE,
        id: `${reward.operator}-${reward.merkleRoot}`,
      }))
      .concat(
        claimedRewards.map((reward) => ({
          ...reward,
          status: REWARD_STATUS.WITHDRAWN,
          id: `${reward.operator}-${reward.merkleRoot}`,
        }))
      )

    yield put({
      type: "rewards/ecdsa_fetch_rewards_data_success",
      payload: {
        totalAvailableAmount,
        availableRewards,
        rewardsHistory,
      },
    })
  } catch (error) {
    yield* logError("rewards/ecdsa_fetch_rewards_data_failure", error)
  }
}

export function* watchFetchECDSARewards() {
  yield takeLatest(
    "rewards/ecdsa_fetch_rewards_data_request",
    fetchECDSARewardsData
  )
}
