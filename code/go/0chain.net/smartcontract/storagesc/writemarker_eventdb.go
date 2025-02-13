package storagesc

import (
	cstate "0chain.net/chaincore/chain/state"
	"0chain.net/chaincore/transaction"
	"0chain.net/smartcontract/dbs/event"
	"github.com/0chain/common/core/currency"
)

// TransactionID and BlockNumber is added at the time of emitting event
func writeMarkerToWriteMarkerTable(wm *WriteMarker, movedTokens currency.Coin, txnHash string) *event.WriteMarker {
	return &event.WriteMarker{
		ClientID:               wm.ClientID,
		BlobberID:              wm.BlobberID,
		AllocationID:           wm.AllocationID,
		AllocationRoot:         wm.AllocationRoot,
		PreviousAllocationRoot: wm.PreviousAllocationRoot,
		FileMetaRoot:           wm.FileMetaRoot,
		Size:                   wm.Size,
		Timestamp:              int64(wm.Timestamp),
		Signature:              wm.Signature,
		MovedTokens:            movedTokens,
		TransactionID:          txnHash,
	}
}

func emitAddWriteMarker(t *transaction.Transaction, wm *WriteMarker, movedTokens currency.Coin,
	balances cstate.StateContextI) {
	balances.EmitEvent(event.TypeStats, event.TagAddWriteMarker,
		t.Hash, writeMarkerToWriteMarkerTable(wm, movedTokens, t.Hash))

	emitUpdateAllocationStatEvent(wm, movedTokens, balances)
	emitUpdateBlobberWriteStatEvent(wm, movedTokens, balances)
}
