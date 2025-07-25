// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE.md

package arbnode

import (
	"context"
	"encoding/binary"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"

	"github.com/offchainlabs/nitro/arbos"
	"github.com/offchainlabs/nitro/arbos/arbostypes"
	"github.com/offchainlabs/nitro/arbos/l2pricing"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/cmd/chaininfo"
	"github.com/offchainlabs/nitro/execution"
	"github.com/offchainlabs/nitro/execution/gethexec"
	"github.com/offchainlabs/nitro/statetransfer"
	"github.com/offchainlabs/nitro/util/arbmath"
	"github.com/offchainlabs/nitro/util/containers"
	"github.com/offchainlabs/nitro/util/testhelpers"
	"github.com/offchainlabs/nitro/util/testhelpers/env"
)

type execClientWrapper struct {
	ExecutionEngine *gethexec.ExecutionEngine
	t               *testing.T
}

func (w *execClientWrapper) Pause() { w.t.Error("not supported") }

func (w *execClientWrapper) Activate() { w.t.Error("not supported") }

func (w *execClientWrapper) ForwardTo(url string) error { w.t.Error("not supported"); return nil }

func (w *execClientWrapper) SequenceDelayedMessage(message *arbostypes.L1IncomingMessage, delayedSeqNum uint64) error {
	return w.ExecutionEngine.SequenceDelayedMessage(message, delayedSeqNum)
}

func (w *execClientWrapper) NextDelayedMessageNumber() (uint64, error) {
	return w.ExecutionEngine.NextDelayedMessageNumber()
}

func (w *execClientWrapper) MarkFeedStart(to arbutil.MessageIndex) containers.PromiseInterface[struct{}] {
	markFeedStartWithReturn := func(to arbutil.MessageIndex) (struct{}, error) {
		w.ExecutionEngine.MarkFeedStart(to)
		return struct{}{}, nil
	}
	return containers.NewReadyPromise(markFeedStartWithReturn(to))
}

func (w *execClientWrapper) ShouldTriggerMaintenance() containers.PromiseInterface[bool] {
	return containers.NewReadyPromise(false, nil)
}

func (w *execClientWrapper) MaintenanceStatus() containers.PromiseInterface[*execution.MaintenanceStatus] {
	return containers.NewReadyPromise(&execution.MaintenanceStatus{}, nil)
}

func (w *execClientWrapper) TriggerMaintenance() containers.PromiseInterface[struct{}] {
	return containers.NewReadyPromise(struct{}{}, nil)
}

func (w *execClientWrapper) Synced(ctx context.Context) bool {
	w.t.Error("not supported")
	return false
}
func (w *execClientWrapper) FullSyncProgressMap(ctx context.Context) map[string]interface{} {
	w.t.Error("not supported")
	return nil
}
func (w *execClientWrapper) SetFinalityData(
	ctx context.Context,
	safeFinalityData *arbutil.FinalityData,
	finalizedFinalityData *arbutil.FinalityData,
	validatedFinalityData *arbutil.FinalityData,
) containers.PromiseInterface[struct{}] {
	return containers.NewReadyPromise(struct{}{}, nil)
}

func (w *execClientWrapper) DigestMessage(num arbutil.MessageIndex, msg *arbostypes.MessageWithMetadata, msgForPrefetch *arbostypes.MessageWithMetadata) containers.PromiseInterface[*execution.MessageResult] {
	return containers.NewReadyPromise(w.ExecutionEngine.DigestMessage(num, msg, msgForPrefetch))
}

func (w *execClientWrapper) Reorg(count arbutil.MessageIndex, newMessages []arbostypes.MessageWithMetadataAndBlockInfo, oldMessages []*arbostypes.MessageWithMetadata) containers.PromiseInterface[[]*execution.MessageResult] {
	return containers.NewReadyPromise(w.ExecutionEngine.Reorg(count, newMessages, oldMessages))
}

func (w *execClientWrapper) HeadMessageIndex() containers.PromiseInterface[arbutil.MessageIndex] {
	return containers.NewReadyPromise(w.ExecutionEngine.HeadMessageIndex())
}

func (w *execClientWrapper) ResultAtMessageIndex(pos arbutil.MessageIndex) containers.PromiseInterface[*execution.MessageResult] {
	return containers.NewReadyPromise(w.ExecutionEngine.ResultAtMessageIndex(pos))
}

func (w *execClientWrapper) Start(ctx context.Context) error {
	return nil
}

func (w *execClientWrapper) MessageIndexToBlockNumber(messageNum arbutil.MessageIndex) containers.PromiseInterface[uint64] {
	return containers.NewReadyPromise(w.ExecutionEngine.MessageIndexToBlockNumber(messageNum), nil)
}

func (w *execClientWrapper) BlockNumberToMessageIndex(blockNum uint64) containers.PromiseInterface[arbutil.MessageIndex] {
	return containers.NewReadyPromise(w.ExecutionEngine.BlockNumberToMessageIndex(blockNum))
}

func (w *execClientWrapper) StopAndWait() {
}

func NewTransactionStreamerForTest(t *testing.T, ctx context.Context, ownerAddress common.Address) (*gethexec.ExecutionEngine, *TransactionStreamer, ethdb.Database, *core.BlockChain) {
	chainConfig := chaininfo.ArbitrumDevTestChainConfig()

	initData := statetransfer.ArbosInitializationInfo{
		Accounts: []statetransfer.AccountInitializationInfo{
			{
				Addr:       ownerAddress,
				EthBalance: big.NewInt(params.Ether),
			},
		},
	}

	chainDb := rawdb.NewMemoryDatabase()
	arbDb := rawdb.NewMemoryDatabase()
	initReader := statetransfer.NewMemoryInitDataReader(&initData)

	cacheConfig := core.DefaultCacheConfigWithScheme(env.GetTestStateScheme())
	bc, err := gethexec.WriteOrTestBlockChain(chainDb, cacheConfig, initReader, chainConfig, nil, nil, arbostypes.TestInitMessage, &gethexec.ConfigDefault.TxIndexer, 0)

	if err != nil {
		Fail(t, err)
	}

	transactionStreamerConfigFetcher := func() *TransactionStreamerConfig { return &DefaultTransactionStreamerConfig }
	execEngine, err := gethexec.NewExecutionEngine(bc, 0)
	if err != nil {
		Fail(t, err)
	}
	stylusTargetConfig := &gethexec.DefaultStylusTargetConfig
	Require(t, stylusTargetConfig.Validate()) // pre-processes config (i.a. parses wasmTargets)
	if err := execEngine.Initialize(gethexec.DefaultCachingConfig.StylusLRUCacheCapacity, &gethexec.DefaultStylusTargetConfig); err != nil {
		Fail(t, err)
	}
	execSeq := &execClientWrapper{execEngine, t}
	inbox, err := NewTransactionStreamer(ctx, arbDb, bc.Config(), execSeq, nil, make(chan error, 1), transactionStreamerConfigFetcher, &DefaultSnapSyncConfig)
	if err != nil {
		Fail(t, err)
	}

	// Add the init message
	err = inbox.AddFakeInitMessage()
	if err != nil {
		Fail(t, err)
	}

	return execEngine, inbox, arbDb, bc
}

type blockTestState struct {
	balances    map[common.Address]*big.Int
	accounts    []common.Address
	numMessages arbutil.MessageIndex
	blockNumber uint64
}

func TestTransactionStreamer(t *testing.T) {
	ownerAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec, inbox, _, bc := NewTransactionStreamerForTest(t, ctx, ownerAddress)

	err := inbox.Start(ctx)
	Require(t, err)
	exec.Start(ctx)

	maxExpectedGasCost := big.NewInt(l2pricing.InitialBaseFeeWei)
	maxExpectedGasCost.Mul(maxExpectedGasCost, big.NewInt(2100*2))

	minBalance := new(big.Int).Mul(maxExpectedGasCost, big.NewInt(100))

	var blockStates []blockTestState
	blockStates = append(blockStates, blockTestState{
		balances: map[common.Address]*big.Int{
			ownerAddress: new(big.Int).SetUint64(params.Ether),
		},
		accounts:    []common.Address{ownerAddress},
		numMessages: 1,
		blockNumber: 0,
	})
	for i := 1; i < 100; i++ {
		if i%10 == 0 {
			reorgTo := rand.Int() % len(blockStates)
			if blockStates[reorgTo].numMessages == 0 {
				Fail(t, "invalid reorg target")
			}
			err := inbox.ReorgAt(blockStates[reorgTo].numMessages)
			if err != nil {
				Fail(t, err)
			}
			blockStates = blockStates[:(reorgTo + 1)]
		} else {
			state := blockStates[len(blockStates)-1]
			newBalances := make(map[common.Address]*big.Int)
			for k, v := range state.balances {
				newBalances[k] = new(big.Int).Set(v)
			}
			state.balances = newBalances

			var messages []arbostypes.MessageWithMetadata
			// TODO replay a random amount of messages too
			numMessages := rand.Int() % 5
			for j := 0; j < numMessages; j++ {
				source := state.accounts[rand.Int()%len(state.accounts)]
				if state.balances[source].Cmp(minBalance) < 0 {
					continue
				}
				value := big.NewInt(int64(rand.Int() % 1000))
				var dest common.Address
				if j == 0 {
					binary.LittleEndian.PutUint64(dest[:], uint64(len(state.accounts)))
					state.accounts = append(state.accounts, dest)
				} else {
					dest = state.accounts[rand.Int()%len(state.accounts)]
				}
				destHash := common.BytesToHash(dest.Bytes())
				var gas uint64 = 100000
				var l2Message []byte
				l2Message = append(l2Message, arbos.L2MessageKind_ContractTx)
				l2Message = append(l2Message, arbmath.Uint64ToU256Bytes(gas)...)
				l2Message = append(l2Message, arbmath.Uint64ToU256Bytes(l2pricing.InitialBaseFeeWei)...)
				l2Message = append(l2Message, destHash.Bytes()...)
				l2Message = append(l2Message, arbmath.U256Bytes(value)...)
				var requestId common.Hash
				binary.BigEndian.PutUint64(requestId.Bytes()[:8], uint64(i))
				messages = append(messages, arbostypes.MessageWithMetadata{
					Message: &arbostypes.L1IncomingMessage{
						Header: &arbostypes.L1IncomingMessageHeader{
							Kind:      arbostypes.L1MessageType_L2Message,
							Poster:    source,
							RequestId: &requestId,
						},
						L2msg: l2Message,
					},
					DelayedMessagesRead: 1,
				})
				state.balances[source].Sub(state.balances[source], value)
				if state.balances[dest] == nil {
					state.balances[dest] = new(big.Int)
				}
				state.balances[dest].Add(state.balances[dest], value)
			}

			Require(t, inbox.AddMessages(state.numMessages, false, messages, nil))

			state.numMessages += arbutil.MessageIndex(len(messages))
			prevBlockNumber := state.blockNumber
			state.blockNumber += uint64(len(messages))
			for i := 0; ; i++ {
				blockNumber := bc.CurrentHeader().Number.Uint64()
				if blockNumber > state.blockNumber {
					Fail(t, "unexpected block number", blockNumber, ">", state.blockNumber)
				} else if blockNumber == state.blockNumber {
					break
				} else if i >= 100 {
					Fail(t, "timed out waiting for new block")
				}
				time.Sleep(10 * time.Millisecond)
			}
			for blockNum := prevBlockNumber + 1; blockNum <= state.blockNumber; blockNum++ {
				block := bc.GetBlockByNumber(blockNum)
				txs := block.Transactions()
				receipts := bc.GetReceiptsByHash(block.Hash())
				if len(txs) != len(receipts) {
					Fail(t, "got", len(txs), "transactions but", len(receipts), "receipts in block", blockNum)
				}
				for i, receipt := range receipts {
					sender, err := types.Sender(types.LatestSigner(bc.Config()), txs[i])
					Require(t, err)
					balance, ok := state.balances[sender]
					if !ok {
						continue
					}
					balance.Sub(balance, arbmath.BigMulByUint(block.BaseFee(), receipt.GasUsed))
				}
			}
			blockStates = append(blockStates, state)
		}

		// Check that state balances are consistent with blockchain's balances
		expectedLastBlockNumber := blockStates[len(blockStates)-1].blockNumber
		for i := 0; ; i++ {
			lastBlockNumber := bc.CurrentHeader().Number.Uint64()
			if lastBlockNumber == expectedLastBlockNumber {
				break
			} else if lastBlockNumber > expectedLastBlockNumber {
				Fail(t, "unexpected block number", lastBlockNumber, "vs", expectedLastBlockNumber)
			} else if i == 10 {
				Fail(t, "timeout waiting for block number", expectedLastBlockNumber, "current", lastBlockNumber)
			}
			time.Sleep(time.Millisecond * 100)
		}

		for _, state := range blockStates {
			block := bc.GetBlockByNumber(state.blockNumber)
			if block == nil {
				Fail(t, "missing state block", state.blockNumber)
			}
			for acct, balance := range state.balances {
				state, err := bc.StateAt(block.Root())
				if err != nil {
					Fail(t, "error getting block state", err)
				}
				haveBalance := state.GetBalance(acct)
				if balance.Cmp(haveBalance.ToBig()) != 0 {
					t.Error("unexpected balance for account", acct, "; expected", balance, "got", haveBalance)
				}
			}
		}
	}
}

func Require(t *testing.T, err error, printables ...interface{}) {
	t.Helper()
	testhelpers.RequireImpl(t, err, printables...)
}

func Fail(t *testing.T, printables ...interface{}) {
	t.Helper()
	testhelpers.FailImpl(t, printables...)
}
