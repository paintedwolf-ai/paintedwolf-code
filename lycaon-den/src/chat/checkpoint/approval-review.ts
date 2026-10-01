import { createSignal } from "solid-js";

export type ApprovalReviewTarget = { sessionId: string; checkpointId: string };
const [approvalReviewTarget, setApprovalReviewTarget] = createSignal<ApprovalReviewTarget | null>(null);
export { approvalReviewTarget };
export function reviewPendingApproval(target: ApprovalReviewTarget): void { setApprovalReviewTarget(target); }
export function consumeApprovalReview(target: ApprovalReviewTarget): void {
  if (approvalReviewTarget() === target) setApprovalReviewTarget(null);
}
