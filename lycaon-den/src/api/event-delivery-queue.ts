import { batch } from "solid-js";
import { latestMessageSnapshot } from "../chat/transcript/projection/messages-equal.ts";
import type { EventEnvelope } from "./types.ts";

import { createEventDeliveryScheduler, type EventDeliveryScheduler } from "./event-delivery-scheduler.ts";

export type EventDeliveryQueue = {
  /** Buffer a delivery; applied retries participate only in checkpoint ordering. */
  enqueue: (envelope: EventEnvelope, alreadyApplied?: boolean) => void;
  /** Apply buffered envelopes now, in arrival order. */
  flush: () => void;
  /** Drop buffered envelopes without applying them. */
  cancel: () => void;
  /** Envelopes waiting for the next drain. */
  pending: () => number;
};

export type EventReceipt = Pick<EventEnvelope, "event_id" | "cursor">;

type EventDeliveryQueueOptions = {
  scheduler?: EventDeliveryScheduler;
  /** Original deliveries represented by a successfully applied snapshot. */
  onApplied?: (receipts: readonly EventReceipt[]) => void;
  /** Last cursor in the fully applied arrival-order prefix, never render order. */
  onCheckpoint?: (cursor: string) => void;
  /** Failed and unapplied deliveries, including superseded message snapshots. */
  onApplyError?: (err: unknown, abandoned: readonly EventReceipt[]) => void;
};

type DeliveryEntry = {
  envelope: EventEnvelope;
  deliveries: EventReceipt[];
  applied: boolean;
};

type Delivery = { receipt: EventReceipt; entry: DeliveryEntry };

/** Row identity for same-batch message collapse. */
function messageRowKey(envelope: EventEnvelope): string | undefined {
  if (envelope.topic !== "message") return undefined;
  return `${envelope.data.session_id}:${envelope.data.message.id}`;
}

// Message rows apply in sequence order to respect the transcript watermark.
// Other topics retain their arrival order.
function sortMessagesInPlace(entries: DeliveryEntry[]): void {
  const slots: number[] = [];
  for (let i = 0; i < entries.length; i += 1) {
    if (entries[i]?.envelope.topic === "message") slots.push(i);
  }
  if (slots.length < 2) return;
  const ordered = slots
    .map((slot) => entries[slot]!)
    .sort((a, b) => {
      const seqA = a.envelope.topic === "message" ? (a.envelope.data.message.seq ?? 0) : 0;
      const seqB = b.envelope.topic === "message" ? (b.envelope.data.message.seq ?? 0) : 0;
      return seqA - seqB;
    });
  slots.forEach((slot, i) => {
    entries[slot] = ordered[i]!;
  });
}

/** Applies a batch in arrival order, sorting message slots by sequence. */
export function createEventDeliveryQueue(
  apply: (envelope: EventEnvelope) => void,
  options: EventDeliveryQueueOptions = {},
): EventDeliveryQueue {
  const scheduler = options.scheduler ?? createEventDeliveryScheduler();
  const pending: DeliveryEntry[] = [];
  const arrivals: Delivery[] = [];
  const rowEntries = new Map<string, DeliveryEntry>();
  const bufferedIds = new Map<string, DeliveryEntry>();
  let handle: number | undefined;

  const clearBuffer = () => {
    pending.length = 0;
    arrivals.length = 0;
    rowEntries.clear();
    bufferedIds.clear();
  };

  const flush = () => {
    if (handle !== undefined) {
      scheduler.cancel(handle);
      handle = undefined;
    }
    if (pending.length === 0) return;
    // Drain before applying: a handler that enqueues lands in the next batch.
    const draining = pending.splice(0);
    const deliveries = arrivals.splice(0);
    rowEntries.clear();
    bufferedIds.clear();
    sortMessagesInPlace(draining);
    let failure: { err: unknown } | undefined;
    batch(() => {
      for (const entry of draining) {
        if (entry.applied) continue;
        try {
          apply(entry.envelope);
          entry.applied = true;
          options.onApplied?.(entry.deliveries);
        } catch (err) {
          failure = { err };
          return;
        }
      }
    });
    let checkpoint: string | undefined;
    for (const delivery of deliveries) {
      if (!delivery.entry.applied) break;
      if (delivery.receipt.cursor) checkpoint = delivery.receipt.cursor;
    }
    if (checkpoint) options.onCheckpoint?.(checkpoint);
    if (failure) {
      options.onApplyError?.(
        failure.err,
        deliveries.filter(({ entry }) => !entry.applied).map(({ receipt }) => receipt),
      );
    }
  };

  return {
    enqueue(envelope, alreadyApplied = false) {
      const duplicate = envelope.event_id ? bufferedIds.get(envelope.event_id) : undefined;
      const rowKey = alreadyApplied ? undefined : messageRowKey(envelope);
      let entry = duplicate ?? (rowKey === undefined ? undefined : rowEntries.get(rowKey));
      if (entry) {
        if (!duplicate && envelope.topic === "message" && entry.envelope.topic === "message") {
          entry.envelope = {
            ...envelope,
            data: {
              ...envelope.data,
              message: latestMessageSnapshot(
                entry.envelope.data.message,
                envelope.data.message,
              ),
            },
          };
        }
      } else {
        entry = { envelope, deliveries: [], applied: alreadyApplied };
        if (rowKey !== undefined) rowEntries.set(rowKey, entry);
        pending.push(entry);
      }
      // Retain checkpoint metadata, not every superseded full-row payload.
      const receipt = { event_id: envelope.event_id, cursor: envelope.cursor };
      entry.deliveries.push(receipt);
      arrivals.push({ receipt, entry });
      if (envelope.event_id) bufferedIds.set(envelope.event_id, entry);
      if (handle === undefined) handle = scheduler.request(flush);
    },
    flush,
    cancel() {
      if (handle !== undefined) {
        scheduler.cancel(handle);
        handle = undefined;
      }
      clearBuffer();
    },
    pending: () => pending.length,
  };
}
