package catalog

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pucora/lura/v2/logging"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Registry holds compiled protocol buffer descriptors loaded from .pb catalog files.
type Registry struct {
	mu    sync.RWMutex
	files *protoregistry.Files
}

// NewRegistry returns an empty catalog registry.
func NewRegistry() *Registry {
	return &Registry{files: &protoregistry.Files{}}
}

// Load reads .pb files and directories from paths and registers their descriptors.
func (r *Registry) Load(paths []string, logger logging.Logger) error {
	if logger == nil {
		logger = logging.NoOp
	}
	var pbFiles []string
	for _, p := range paths {
		files, err := collectPBFiles(p)
		if err != nil {
			return err
		}
		pbFiles = append(pbFiles, files...)
	}
	if len(pbFiles) == 0 {
		return fmt.Errorf("grpc catalog: no .pb files found in %v", paths)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	merged := &descriptorpb.FileDescriptorSet{}
	seen := map[string]struct{}{}
	for _, path := range pbFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("grpc catalog: read %s: %w", path, err)
		}
		fds := &descriptorpb.FileDescriptorSet{}
		if err := proto.Unmarshal(data, fds); err != nil {
			return fmt.Errorf("grpc catalog: unmarshal %s: %w", path, err)
		}
		for _, fd := range fds.File {
			name := fd.GetName()
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			merged.File = append(merged.File, fd)
		}
	}

	files := &protoregistry.Files{}
	for _, fd := range merged.File {
		file, err := protodesc.NewFile(fd, files)
		if err != nil {
			logger.Warning("[SERVICE: gRPC]", "catalog missing dependency for", fd.GetName()+":", err.Error())
			continue
		}
		if err := files.RegisterFile(file); err != nil {
			return fmt.Errorf("grpc catalog: register %s: %w", fd.GetName(), err)
		}
	}
	r.files = files
	return nil
}

func collectPBFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(path), ".pb") {
			return []string{path}, nil
		}
		return nil, fmt.Errorf("grpc catalog: %s is not a .pb file", path)
	}
	var out []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".pb") {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// LookupMethod resolves a full gRPC method path such as /flight_finder.Flights/FindFlight.
func (r *Registry) LookupMethod(fullMethod string) (protoreflect.MethodDescriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.files == nil {
		return nil, fmt.Errorf("grpc catalog: registry not loaded")
	}
	fullMethod = strings.TrimPrefix(fullMethod, "/")
	parts := strings.Split(fullMethod, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("grpc catalog: invalid method %q", fullMethod)
	}
	serviceName, methodName := parts[0], parts[1]

	var found protoreflect.MethodDescriptor
	r.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			if string(svc.FullName()) != serviceName {
				continue
			}
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				m := methods.Get(j)
				if string(m.Name()) == methodName {
					found = m
					return false
				}
			}
		}
		return true
	})
	if found == nil {
		return nil, fmt.Errorf("grpc catalog: method not found: /%s/%s", serviceName, methodName)
	}
	return found, nil
}

// Files returns the underlying descriptor registry (for reflection registration).
func (r *Registry) Files() *protoregistry.Files {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.files
}

// LookupService finds a service descriptor by full name.
func (r *Registry) LookupService(serviceName string) (protoreflect.ServiceDescriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.files == nil {
		return nil, fmt.Errorf("grpc catalog: registry not loaded")
	}
	var found protoreflect.ServiceDescriptor
	r.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			if string(svc.FullName()) == serviceName {
				found = svc
				return false
			}
		}
		return true
	})
	if found == nil {
		return nil, fmt.Errorf("grpc catalog: service not found: %s", serviceName)
	}
	return found, nil
}
