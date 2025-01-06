package firehose

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/cmd/tools/print"
	"github.com/streamingfast/firehose-core/types"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
)

func NewToolsFirehoseClientCmd[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firehose-client <endpoint> <range>",
		Short: "Connects to a Firehose endpoint over gRPC and print block stream as JSON to terminal",
		Args:  cobra.ExactArgs(2),
		RunE:  getFirehoseClientE(chain, logger),
	}

	addFirehoseStreamClientFlagsToSet(cmd.Flags(), chain)

	cmd.Flags().Bool("final-blocks-only", false, "Only ask for final blocks")
	cmd.Flags().Bool("print-cursor-only", false, "Skip block decoding, only print the step cursor (useful for performance testing)")

	return cmd
}

type respChan struct {
	ch chan string
}

func getFirehoseClientE[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		firehoseClient, connClose, requestInfo, err := getFirehoseStreamClientFromCmd(cmd, rootLog, args[0], chain)
		if err != nil {
			return err
		}
		defer connClose()

		blockRange, err := types.GetBlockRangeFromArg(args[1])
		if err != nil {
			return fmt.Errorf("invalid range %q: %w", args[1], err)
		}

		printCursorOnly := sflags.MustGetBool(cmd, "print-cursor-only")

		request := &pbfirehose.Request{
			StartBlockNum:   blockRange.Start,
			StopBlockNum:    blockRange.GetStopBlockOr(0),
			Transforms:      requestInfo.Transforms,
			FinalBlocksOnly: requestInfo.FinalBlocksOnly,
			Cursor:          requestInfo.Cursor,
		}

		stream, err := firehoseClient.Blocks(ctx, request, requestInfo.GRPCCallOpts...)
		if err != nil {
			return fmt.Errorf("unable to start blocks stream: %w", err)
		}

		meta, err := stream.Header()
		if err != nil {
			rootLog.Warn("cannot read header")
		} else {
			if hosts := meta.Get("hostname"); len(hosts) != 0 {
				rootLog = rootLog.With(zap.String("remote_hostname", hosts[0]))
			}
		}
		rootLog.Info("connected")

		resps := make(chan *respChan, 10)
		allDone := make(chan bool)

		if !printCursorOnly {
			// print the responses linearly
			go func() {
				for resp := range resps {
					line := <-resp.ch
					fmt.Println(line)
				}
				close(allDone)
			}()
		}

		printer, err := print.GetOutputPrinter(cmd, chain.BlockFileDescriptor())
		if err != nil {
			return fmt.Errorf("unable to create output printer: %w", err)
		}

		for {
			response, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("stream error while receiving: %w", err)
			}

			if printCursorOnly {
				fmt.Printf("%s - %s\n", response.Step.String(), response.Cursor)
				continue
			}

			resp := &respChan{
				ch: make(chan string),
			}
			resps <- resp

			// async process the response
			go func() {
				buffer := bytes.NewBuffer(nil)
				err := printer.PrintTo(response, buffer)
				if err != nil {
					rootLog.Error("marshalling to string", zap.Error(err))
					resp.ch <- ""
					return
				}

				resp.ch <- buffer.String()
			}()
		}
		if printCursorOnly {
			return nil
		}

		close(resps)
		<-allDone
		return nil
	}
}
