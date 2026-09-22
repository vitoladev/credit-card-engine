# Revolving Credit Evaluation

Simulates whether a credit card customer may be granted revolving credit, and
how much, from a fictional customer profile.

## Language

### Customer profile

**Customer**:
A credit card holder being evaluated, identified by CPF.
_Avoid_: Client, user, account

**Credit limit**:
The total limit of the customer's card. The requirement's "limite disponível"
means this value.
_Avoid_: Available limit

**Current invoice**:
The amount billed on the customer's card in the open cycle.
_Avoid_: Bill, statement

**Late payments**:
The number of invoices the customer paid after the due date.
_Avoid_: Delays, delinquencies

**Spend history**:
The customer's monthly card spend, oldest first.
_Avoid_: Spending behavior, transactions

### Decision

**Rule**:
One eligibility criterion. It either denies with a reason or passes.

**Policy**:
The ordered set of rules plus the amount calculation that the product applies.
The first rule that denies decides.

**Evaluation**:
Applying the policy to one customer.

**Decision**:
The outcome of an evaluation: approved or denied.
_Avoid_: Verdict, status

**Reason**:
A stable code that explains a decision (`eligible` or the denying rule's code).

**Revolving amount**:
The maximum revolving credit granted to an approved customer.
_Avoid_: Released value, max amount

**Single evaluation**:
An evaluation requested for one customer, answered synchronously. It never
belongs to a batch.

### Batches

**Batch**:
A list of customers submitted together for asynchronous evaluation. It is
accepted whole or rejected whole.

**Report**:
The aggregated view of one batch: approved and denied customers and the total
revolving amount. A report has no identity of its own; it belongs to its batch.
_Avoid_: Snapshot

**Batch item**:
One customer inside a batch, identified by its position. It ends with a
decision, or it fails and waits for an operator.
_Avoid_: Job, message

**Failed item**:
A batch item whose evaluation could not complete after automatic retries. It
has no decision. An operator retries it or cancels it.
_Avoid_: Failed decision

**Item status**:
Where a batch item stands: queued, decided, failed or cancelled. Only a failed
item can be retried or cancelled.

**Attempt**:
One pass of a batch item through evaluation, the first one or an operator
retry. A batch item gets at most 5 attempts.

**Batch status**:
Derived from its items: processing while any item is queued, needs attention
when none is queued and some item failed, completed when every item is decided
or cancelled.
_Avoid_: Failed batch, cancelled batch

**Invalid customer**:
A customer whose data fails validation (for example a CPF with wrong check
digits). An invalid customer is never evaluated and gets no decision.
_Avoid_: Rejected, denied
