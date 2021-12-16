// Code generated by protoc-gen-go. DO NOT EDIT.
// source: voltha_protos/omci_test.proto

package omci

import (
	fmt "fmt"
	proto "github.com/golang/protobuf/proto"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion3 // please upgrade the proto package

type TestResponse_TestResponseResult int32

const (
	TestResponse_SUCCESS TestResponse_TestResponseResult = 0
	TestResponse_FAILURE TestResponse_TestResponseResult = 1
)

var TestResponse_TestResponseResult_name = map[int32]string{
	0: "SUCCESS",
	1: "FAILURE",
}

var TestResponse_TestResponseResult_value = map[string]int32{
	"SUCCESS": 0,
	"FAILURE": 1,
}

func (x TestResponse_TestResponseResult) String() string {
	return proto.EnumName(TestResponse_TestResponseResult_name, int32(x))
}

func (TestResponse_TestResponseResult) EnumDescriptor() ([]byte, []int) {
	return fileDescriptor_146dc5f97baf9397, []int{1, 0}
}

type OmciTestRequest struct {
	Id                   string   `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	Uuid                 string   `protobuf:"bytes,2,opt,name=uuid,proto3" json:"uuid,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *OmciTestRequest) Reset()         { *m = OmciTestRequest{} }
func (m *OmciTestRequest) String() string { return proto.CompactTextString(m) }
func (*OmciTestRequest) ProtoMessage()    {}
func (*OmciTestRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_146dc5f97baf9397, []int{0}
}

func (m *OmciTestRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_OmciTestRequest.Unmarshal(m, b)
}
func (m *OmciTestRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_OmciTestRequest.Marshal(b, m, deterministic)
}
func (m *OmciTestRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_OmciTestRequest.Merge(m, src)
}
func (m *OmciTestRequest) XXX_Size() int {
	return xxx_messageInfo_OmciTestRequest.Size(m)
}
func (m *OmciTestRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_OmciTestRequest.DiscardUnknown(m)
}

var xxx_messageInfo_OmciTestRequest proto.InternalMessageInfo

func (m *OmciTestRequest) GetId() string {
	if m != nil {
		return m.Id
	}
	return ""
}

func (m *OmciTestRequest) GetUuid() string {
	if m != nil {
		return m.Uuid
	}
	return ""
}

type TestResponse struct {
	Result               TestResponse_TestResponseResult `protobuf:"varint,1,opt,name=result,proto3,enum=omci.TestResponse_TestResponseResult" json:"result,omitempty"`
	XXX_NoUnkeyedLiteral struct{}                        `json:"-"`
	XXX_unrecognized     []byte                          `json:"-"`
	XXX_sizecache        int32                           `json:"-"`
}

func (m *TestResponse) Reset()         { *m = TestResponse{} }
func (m *TestResponse) String() string { return proto.CompactTextString(m) }
func (*TestResponse) ProtoMessage()    {}
func (*TestResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_146dc5f97baf9397, []int{1}
}

func (m *TestResponse) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_TestResponse.Unmarshal(m, b)
}
func (m *TestResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_TestResponse.Marshal(b, m, deterministic)
}
func (m *TestResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_TestResponse.Merge(m, src)
}
func (m *TestResponse) XXX_Size() int {
	return xxx_messageInfo_TestResponse.Size(m)
}
func (m *TestResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_TestResponse.DiscardUnknown(m)
}

var xxx_messageInfo_TestResponse proto.InternalMessageInfo

func (m *TestResponse) GetResult() TestResponse_TestResponseResult {
	if m != nil {
		return m.Result
	}
	return TestResponse_SUCCESS
}

func init() {
	proto.RegisterEnum("omci.TestResponse_TestResponseResult", TestResponse_TestResponseResult_name, TestResponse_TestResponseResult_value)
	proto.RegisterType((*OmciTestRequest)(nil), "omci.OmciTestRequest")
	proto.RegisterType((*TestResponse)(nil), "omci.TestResponse")
}

func init() { proto.RegisterFile("voltha_protos/omci_test.proto", fileDescriptor_146dc5f97baf9397) }

var fileDescriptor_146dc5f97baf9397 = []byte{
	// 230 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0x92, 0x2d, 0xcb, 0xcf, 0x29,
	0xc9, 0x48, 0x8c, 0x2f, 0x28, 0xca, 0x2f, 0xc9, 0x2f, 0xd6, 0xcf, 0xcf, 0x4d, 0xce, 0x8c, 0x2f,
	0x49, 0x2d, 0x2e, 0xd1, 0x03, 0x0b, 0x08, 0xb1, 0x80, 0x04, 0x94, 0x4c, 0xb9, 0xf8, 0xfd, 0x73,
	0x93, 0x33, 0x43, 0x52, 0x8b, 0x4b, 0x82, 0x52, 0x0b, 0x4b, 0x53, 0x8b, 0x4b, 0x84, 0xf8, 0xb8,
	0x98, 0x32, 0x53, 0x24, 0x18, 0x15, 0x18, 0x35, 0x38, 0x83, 0x98, 0x32, 0x53, 0x84, 0x84, 0xb8,
	0x58, 0x4a, 0x4b, 0x33, 0x53, 0x24, 0x98, 0xc0, 0x22, 0x60, 0xb6, 0x52, 0x2d, 0x17, 0x0f, 0x44,
	0x4b, 0x71, 0x41, 0x7e, 0x5e, 0x71, 0xaa, 0x90, 0x2d, 0x17, 0x5b, 0x51, 0x6a, 0x71, 0x69, 0x4e,
	0x09, 0x58, 0x1f, 0x9f, 0x91, 0xaa, 0x1e, 0xc8, 0x74, 0x3d, 0x64, 0x35, 0x28, 0x9c, 0x20, 0xb0,
	0xe2, 0x20, 0xa8, 0x26, 0x25, 0x3d, 0x2e, 0x21, 0x4c, 0x59, 0x21, 0x6e, 0x2e, 0xf6, 0xe0, 0x50,
	0x67, 0x67, 0xd7, 0xe0, 0x60, 0x01, 0x06, 0x10, 0xc7, 0xcd, 0xd1, 0xd3, 0x27, 0x34, 0xc8, 0x55,
	0x80, 0xd1, 0xc9, 0x83, 0x4b, 0x22, 0xbf, 0x28, 0x5d, 0x2f, 0xbf, 0x20, 0x35, 0x2f, 0x39, 0xbf,
	0x28, 0x45, 0x0f, 0xe2, 0x53, 0xb0, 0x9d, 0x51, 0x3a, 0xe9, 0x99, 0x25, 0x19, 0xa5, 0x49, 0x7a,
	0xc9, 0xf9, 0xb9, 0xfa, 0x30, 0x05, 0xfa, 0x10, 0x05, 0xba, 0xd0, 0xa0, 0x28, 0x33, 0xd5, 0x4f,
	0xcf, 0x07, 0x07, 0x48, 0x12, 0x1b, 0x58, 0xc8, 0x18, 0x10, 0x00, 0x00, 0xff, 0xff, 0x85, 0xc9,
	0x07, 0x50, 0x2d, 0x01, 0x00, 0x00,
}