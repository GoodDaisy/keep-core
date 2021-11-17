import React, { useMemo, useState } from "react"
import { withBaseModal } from "../withBaseModal"
import { ModalBody, ModalFooter, ModalHeader } from "../Modal"
import Button, { SubmitButton } from "../../Button"
import { useFormik } from "formik"
import useReleaseTokens from "../../../hooks/useReleaseTokens"
import {
  Accordion,
  AccordionItem,
  AccordionItemButton,
  AccordionItemHeading,
  AccordionItemPanel,
} from "react-accessible-accordion"
import { KEEP } from "../../../utils/token.utils"
import TokenAmount from "../../TokenAmount"
import { shortenAddress } from "../../../utils/general.utils"
import moment from "moment"
import { add } from "../../../utils/arithmetics.utils"
import OnlyIf from "../../OnlyIf"

export const WithdrawGrantedTokens = withBaseModal(({ grants, onClose }) => {
  const [numberOfGrantsDisplayed, setNumberOfGrantsDisplayed] = useState(5)
  const releaseTokens = useReleaseTokens()

  const totalReadyToRelease = useMemo(() => {
    return grants
      .map((grant) => grant.readyToRelease)
      .reduce((previous, current) => add(previous, current), 0)
      .toString()
  }, [grants])

  const onWithdrawClick = (awaitingPromise) => {
    const selectedTokenGrant = grants.find(
      (grant) => grant.id === formik.values.selectedGrantId
    )
    releaseTokens(selectedTokenGrant, awaitingPromise)
  }

  const formik = useFormik({
    enableReinitialize: true,
    initialValues: {
      selectedGrantId: null,
    },
    onSubmit: (values) => {
      alert(JSON.stringify(values, null, 2))
    },
  })

  return (
    <>
      <ModalHeader>Withdraw</ModalHeader>
      <ModalBody>
        <h3 className={"mb-1"}>Withdraw granted tokens</h3>
        <p className={"mb-2"}>
          The following tokens are available to be released and withdrawn from
          your token grant.
        </p>
        <div>
          <div className="withdraw-granted-tokens__info-row">
            <h4 className="withdraw-granted-tokens__info-row-title">
              Withdraw:
            </h4>
            <span className="withdraw-granted-tokens__info-row-value">
              total:{" "}
              <TokenAmount
                amount={totalReadyToRelease}
                token={KEEP}
                wrapperClassName="withdraw-granted-tokens__total-ready-to-release-amount"
                amountClassName="text-grey-60"
                symbolClassName="text-grey-60"
              />
            </span>
          </div>
          <form>
            {grants
              .slice(0, numberOfGrantsDisplayed)
              .map((grant) => renderGrant(grant, grants.length, formik))}
          </form>
          <OnlyIf condition={grants.length > numberOfGrantsDisplayed}>
            <div
              className="withdraw-granted-tokens__view-more-container"
              onClick={() =>
                setNumberOfGrantsDisplayed(numberOfGrantsDisplayed + 5)
              }
            >
              <span>+&nbsp;</span>
              View More
            </div>
          </OnlyIf>
        </div>
      </ModalBody>
      <ModalFooter>
        <SubmitButton
          className="btn btn-primary btn-lg mr-2"
          type="submit"
          onSubmitAction={(awaitingPromise) => {
            onWithdrawClick(awaitingPromise)
          }}
          disabled={!formik.values.selectedGrantId}
        >
          withdraw
        </SubmitButton>
        <Button className={`btn btn-unstyled text-link`} onClick={onClose}>
          Cancel
        </Button>
      </ModalFooter>
    </>
  )
})

const renderGrant = (grant, totalNumberOfGrants, formik) => {
  return (
    <div
      key={`Grant-${grant.id}`}
      style={{
        display: "flex",
        alignItems: "flex-start",
      }}
      className={"withdraw-granted-tokens__grants-accordion-container"}
    >
      <input
        className="radio-without-label"
        type="radio"
        name="selectedGrantId"
        value={grant.id}
        id={`grant-${grant.id}`}
        checked={formik.values.selectedGrantId === grant.id}
        onChange={() => {
          formik.setFieldValue("selectedGrantId", grant.id)
        }}
      />
      <label htmlFor={`grant-${grant.id}`} />
      <Accordion
        allowZeroExpanded={totalNumberOfGrants > 1}
        className={"withdraw-granted-tokens__grants-accordion"}
      >
        <AccordionItem
          {...(totalNumberOfGrants === 1
            ? { dangerouslySetExpanded: true }
            : {})}
        >
          <AccordionItemHeading>
            <AccordionItemButton>
              <div className="withdraw-granted-tokens__token-amount">
                <TokenAmount
                  amount={grant.readyToRelease}
                  amountClassName={"h4 text-mint-100"}
                  symbolClassName={"h4 text-mint-100"}
                  token={KEEP}
                />
              </div>
              <span className="withdraw-granted-tokens__details-text">
                Details
              </span>
              <OnlyIf condition={totalNumberOfGrants > 1}>
                <span className="withdraw-granted-tokens__expand-button" />
              </OnlyIf>
            </AccordionItemButton>
          </AccordionItemHeading>
          <AccordionItemPanel>
            <div className="withdraw-granted-tokens__info-row">
              <span className="withdraw-granted-tokens__info-row-title withdraw-granted-tokens__info-row-title--small">
                token grant id
              </span>
              <span className="withdraw-granted-tokens__info-row-value withdraw-granted-tokens__info-row-value--small">
                {grant.id}
              </span>
            </div>
            <div className="withdraw-granted-tokens__info-row">
              <span className="withdraw-granted-tokens__info-row-title withdraw-granted-tokens__info-row-title--small">
                date issued
              </span>
              <span className="withdraw-granted-tokens__info-row-value withdraw-granted-tokens__info-row-value--small">
                {moment.unix(grant.start).format("MM/DD/YYYY")}
              </span>
            </div>
            <div className="withdraw-granted-tokens__info-row">
              <span className="withdraw-granted-tokens__info-row-title withdraw-granted-tokens__info-row-title--small">
                wallet
              </span>
              <span className="withdraw-granted-tokens__info-row-value withdraw-granted-tokens__info-row-value--small">
                {shortenAddress(grant.grantee)}
              </span>
            </div>
          </AccordionItemPanel>
        </AccordionItem>
      </Accordion>
    </div>
  )
}
