package batcher

import (
	"context"
	"fmt"

	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum-optimism/optimism/op-node/rollup"
	"github.com/ethereum-optimism/optimism/op-service/txmgr"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"
)

// FramePublisher transforms txData structs into txmgr.TxCandidate structs and queues them for sending.
// It is responsible for sending transactions to the L1 chain.
type FramePublisher struct {
	log          log.Logger
	rollupConfig *rollup.Config
	altDA        *altda.DAClient

	// below need to be initialized dynamically by Start()
	daErrGroup errgroup.Group
	state      *channelManager
	queue      *txmgr.Queue[txRef]
	receiptsCh chan txmgr.TxReceipt[txRef]
}

func NewFramePublisher(
	log log.Logger,
	rollupConfig *rollup.Config,
	altDA *altda.DAClient,
) *FramePublisher {
	return &FramePublisher{
		log:          log,
		rollupConfig: rollupConfig,
		altDA:        altDA,
	}
}

// TODO: this is super super ugly... must be a better way to construct/initialize this
// but for now just want to focus on the idea of the FramePublisher
func (fp *FramePublisher) Init(killCtx context.Context, txMgr txmgr.TxManager, state *channelManager, maxPendingDaPutRequests uint64, maxPendingEthTxs uint64) {
	receiptsCh := make(chan txmgr.TxReceipt[txRef])
	queue := txmgr.NewQueue[txRef](killCtx, txMgr, maxPendingEthTxs)
	daErrGroup := errgroup.Group{}
	daErrGroup.SetLimit(int(maxPendingDaPutRequests))
	fp.receiptsCh = receiptsCh
	fp.queue = queue
	fp.state = state
}

// Publish creates & queues for sending a transaction to the batch inbox address with the given `txData`.
// The method will block if the queue's MaxPendingTransactions is exceeded.
func (fp *FramePublisher) Publish(ctx context.Context, txdata txData) error {
	var err error

	// if Alt DA is enabled we post the txdata to the DA Provider and replace it with the commitment.
	if fp.altDA != nil {
		if txdata.asBlob {
			return fmt.Errorf("AltDA with 4844 blob txs not supported")
		}
		// sanity check
		if nf := len(txdata.frames); nf != 1 {
			fp.log.Crit("Unexpected number of frames in calldata tx", "num_frames", nf)
		}
		// when posting txdata to an external DA Provider, we use a goroutine to avoid blocking the main loop
		// since it may take a while to post the txdata to the DA Provider.
		fp.daErrGroup.Go(func() error {
			comm, err := fp.altDA.SetInput(ctx, txdata.CallData())
			if err != nil {
				fp.log.Error("Failed to post input to Alt DA", "error", err)
				// requeue frame if we fail to post to the DA Provider so it can be retried
				fp.recordFailedTx(txdata.ID(), err)
				return err
			}
			fp.log.Info("Set altda input", "commitment", comm, "tx", txdata.ID())
			// signal altda commitment tx with TxDataVersion1
			candidate := fp.calldataTxCandidate(comm.TxData())
			fp.queueTx(txdata, false, candidate)
			return nil
		})
		// we return nil to allow publishStateToL1 to keep processing the next txdata
		return nil
	}

	var candidate *txmgr.TxCandidate
	if txdata.asBlob {
		if candidate, err = fp.blobTxCandidate(txdata); err != nil {
			// We could potentially fall through and try a calldata tx instead, but this would
			// likely result in the chain spending more in gas fees than it is tuned for, so best
			// to just fail. We do not expect this error to trigger unless there is a serious bug
			// or configuration issue.
			return fmt.Errorf("could not create blob tx candidate: %w", err)
		}
	} else {
		// sanity check
		if nf := len(txdata.frames); nf != 1 {
			fp.log.Crit("Unexpected number of frames in calldata tx", "num_frames", nf)
		}
		candidate = fp.calldataTxCandidate(txdata.CallData())
	}

	fp.queueTx(txdata, false, candidate)
	return nil
}

func (fp *FramePublisher) calldataTxCandidate(data []byte) *txmgr.TxCandidate {
	fp.log.Info("Building Calldata transaction candidate", "size", len(data))
	return &txmgr.TxCandidate{
		To:     &fp.rollupConfig.BatchInboxAddress,
		TxData: data,
	}
}

func (fp *FramePublisher) blobTxCandidate(data txData) (*txmgr.TxCandidate, error) {
	blobs, err := data.Blobs()
	if err != nil {
		return nil, fmt.Errorf("generating blobs for tx data: %w", err)
	}
	size := data.Len()
	lastSize := len(data.frames[len(data.frames)-1].data)
	fp.log.Info("Building Blob transaction candidate",
		"size", size, "last_size", lastSize, "num_blobs", len(blobs))
	// TODO: move metric from driver to here
	// fp.Metr.RecordBlobUsedBytes(lastSize)
	return &txmgr.TxCandidate{
		To:    &fp.rollupConfig.BatchInboxAddress,
		Blobs: blobs,
	}, nil
}

func (fp *FramePublisher) queueTx(txdata txData, isCancel bool, candidate *txmgr.TxCandidate) {
	intrinsicGas, err := core.IntrinsicGas(candidate.TxData, nil, false, true, true, false)
	if err != nil {
		// we log instead of return an error here because txmgr can do its own gas estimation
		fp.log.Error("Failed to calculate intrinsic gas", "err", err)
	} else {
		candidate.GasLimit = intrinsicGas
	}

	fp.queue.Send(txRef{id: txdata.ID(), isCancel: isCancel, isBlob: txdata.asBlob}, *candidate, fp.receiptsCh)
}

func (l *FramePublisher) recordFailedTx(id txID, err error) {
	l.log.Warn("Transaction failed to send", logFields(id, err)...)
	l.state.TxFailed(id)
}

func (fp *FramePublisher) Wait() {
	fp.log.Info("Wait for pure DA writes, not L1 txs")
	fp.daErrGroup.Wait()
	fp.log.Info("Wait for L1 writes (blobs or DA commitments)")
	fp.queue.Wait()
}
