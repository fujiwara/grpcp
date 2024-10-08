package grpcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	pb "github.com/fujiwara/grpcp/proto"
	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedFileTransferServiceServer
}

var (
	StreamBufferSize = 1024 * 1024
)

func (s *server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	slog.Info("ping", "message", req.Message)
	return &pb.PingResponse{Message: "pong"}, nil
}

func newUploadResponse(msg string) *pb.FileUploadResponse {
	return &pb.FileUploadResponse{Message: msg}
}

func (s *server) Upload(stream pb.FileTransferService_UploadServer) error {
	if err := s.upload(stream); err != nil {
		slog.Error(err.Error())
		return err
	}
	return nil
}

func (s *server) upload(stream pb.FileTransferService_UploadServer) error {
	var once sync.Once
	var f *os.File
	var totalBytes, expectedSize int64
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			slog.Info("server upload completed", "bytes", totalBytes)
			if totalBytes != expectedSize {
				return fmt.Errorf("file size mismatch: expected %d bytes, got %d bytes", expectedSize, totalBytes)
			}
			return stream.SendAndClose(newUploadResponse("Upload received successfully"))
		} else if err != nil {
			return fmt.Errorf("failed to receive file: %w", err)
		}
		once.Do(func() {
			slog.Info("server accepting upload request", "filename", req.Filename, "bytes", req.Size)
			f, err = os.OpenFile(req.Filename, os.O_WRONLY|os.O_CREATE, 0644)
			expectedSize = req.Size
		})
		if err != nil || f == nil {
			return fmt.Errorf("failed to open file: %w", err)
		}
		if n, err := f.Write(req.Content); err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		} else {
			totalBytes += int64(n)
		}
	}
}

func (s *server) Download(req *pb.FileDownloadRequest, stream pb.FileTransferService_DownloadServer) error {
	if err := s.download(req, stream); err != nil {
		slog.Error(err.Error())
		return err
	}
	return nil
}

func (s *server) download(req *pb.FileDownloadRequest, stream pb.FileTransferService_DownloadServer) error {
	slog.Info("server accepting download request", "filename", req.Filename)
	f, err := os.Open(req.Filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}
	expectedBytes := st.Size()
	totalBytes := int64(0)
	buf := make([]byte, StreamBufferSize)
	for {
		n, err := f.Read(buf)
		if err == io.EOF {
			slog.Info("server download completed", "bytes", totalBytes)
			if totalBytes != expectedBytes {
				return fmt.Errorf("file size mismatch: expected %d bytes, got %d bytes", expectedBytes, totalBytes)
			}
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
		if err := stream.Send(&pb.FileDownloadResponse{
			Filename: req.Filename,
			Content:  buf[:n],
			Size:     expectedBytes,
		}); err != nil {
			return fmt.Errorf("failed to send file: %w", err)
		}
		totalBytes += int64(n)
	}
}

func (s *server) Shutdown(ctx context.Context, req *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	slog.Info("server shutdown requested")
	go func() {
		tm := time.NewTimer(time.Second)
		<-tm.C
		slog.Info("server shutdown completed")
		os.Exit(0)
	}()
	return &pb.ShutdownResponse{}, nil
}

func newListener(addr string, opt *ServerOption) (net.Listener, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}
	if !opt.TLS {
		slog.Warn("running server without TLS")
		return lis, nil
	}

	var tlsConfig *tls.Config
	if opt.CertFile == "" || opt.KeyFile == "" {
		slog.Info("generating self-signed certificate")
		tlsConfig, err = genSelfSignedTLS()
		if err != nil {
			return nil, fmt.Errorf("failed to generate tls config: %w", err)
		}
	} else {
		slog.Info("loading certificate", "cert", opt.CertFile, "key", opt.KeyFile)
		tlsConfig, err = genTLS(opt.CertFile, opt.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to generate tls config: %w", err)
		}
	}
	return tls.NewListener(lis, tlsConfig), nil
}

func RunServer(ctx context.Context, opt *ServerOption) error {
	s := grpc.NewServer()
	addr := fmt.Sprintf("%s:%d", opt.Listen, opt.Port)
	lis, err := newListener(addr, opt)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}
	slog.Info("starting server", "addr", addr, "tls", opt.TLS)
	pb.RegisterFileTransferServiceServer(s, &server{})
	if err := s.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve: %w", err)
	}
	return nil
}
