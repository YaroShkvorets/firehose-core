package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"google.golang.org/protobuf/proto"

	"buf.build/gen/go/bufbuild/reflect/connectrpc/go/buf/reflect/v1beta1/reflectv1beta1connect"
	reflectv1beta1 "buf.build/gen/go/bufbuild/reflect/protocolbuffers/go/buf/reflect/v1beta1"
	connect "connectrpc.com/connect"
	"github.com/iancoleman/strcase"
	"github.com/streamingfast/cli"
)

//go:embed *.gotmpl
var templates embed.FS

var wellKnownProtoRepos = []string{
	"buf.build/streamingfast/firehose-ethereum",
	"buf.build/streamingfast/firehose-near",
	"buf.build/streamingfast/firehose-solana",
	//"buf.build/streamingfast/firehose-bitcoin",
}

func main() {
	cli.Ensure(len(os.Args) == 3, "go run ./generator <output_file> <package_name>")

	authToken := os.Getenv("BUFBUILD_AUTH_TOKEN")
	if authToken == "" {
		log.Fatalf("Please set the BUFBUILD_AUTH_TOKEN environment variable, to generate well known registry")
		return
	}

	output := os.Args[1]
	packageName := os.Args[2]

	client := reflectv1beta1connect.NewFileDescriptorSetServiceClient(
		http.DefaultClient,
		"https://buf.build",
	)

	var protofiles []ProtoFile

	for _, wellKnownProtoRepo := range wellKnownProtoRepos {
		request := connect.NewRequest(&reflectv1beta1.GetFileDescriptorSetRequest{
			Module: wellKnownProtoRepo,
		})
		request.Header().Set("Authorization", "Bearer "+authToken)
		fileDescriptorSet, err := client.GetFileDescriptorSet(context.Background(), request)
		if err != nil {
			log.Fatalf("failed to call file descriptor set service: %v", err)
			return
		}

		for _, file := range fileDescriptorSet.Msg.FileDescriptorSet.File {
			cnt, err := proto.Marshal(file)
			if err != nil {
				log.Fatalf("failed to marshall proto file %s: %v", file.GetName(), err)
				return
			}
			name := ""
			if file.Name != nil {
				name = *file.Name
			}
			protofiles = append(protofiles, ProtoFile{name, cnt})
		}
		// avoid hitting the buf.build rate limit
		time.Sleep(1 * time.Second)
	}

	tmpl, err := template.New("wellknown").Funcs(templateFunctions()).ParseFS(templates, "*.gotmpl")
	cli.NoError(err, "Unable to instantiate template")

	var out io.Writer = os.Stdout
	if output != "-" {
		cli.NoError(os.MkdirAll(filepath.Dir(output), os.ModePerm), "Unable to create output file directories")

		file, err := os.Create(output)
		cli.NoError(err, "Unable to open output file")

		bufferedOut := bufio.NewWriter(file)
		out = bufferedOut

		defer func() {
			bufferedOut.Flush()
			file.Close()
		}()
	}

	err = tmpl.ExecuteTemplate(out, "template.gotmpl", map[string]any{
		"Package":    packageName,
		"ProtoFiles": protofiles,
	})
	cli.NoError(err, "Unable to render template")

	fmt.Println("Done creating well known registry")
}

type ProtoFile struct {
	Name string
	Data []byte
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"lower":      strings.ToLower,
		"pascalCase": strcase.ToCamel,
		"camelCase":  strcase.ToLowerCamel,
		"toHex": func(in []byte) string {
			return hex.EncodeToString(in)
		},
	}
}
