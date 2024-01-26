// Code generated by counterfeiter. DO NOT EDIT.
package servicefakes

import (
	"context"
	"sync"

	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/protocol/livekit"
)

type FakeSIPStore struct {
	DeleteSIPDispatchRuleStub        func(context.Context, *livekit.SIPDispatchRuleInfo) error
	deleteSIPDispatchRuleMutex       sync.RWMutex
	deleteSIPDispatchRuleArgsForCall []struct {
		arg1 context.Context
		arg2 *livekit.SIPDispatchRuleInfo
	}
	deleteSIPDispatchRuleReturns struct {
		result1 error
	}
	deleteSIPDispatchRuleReturnsOnCall map[int]struct {
		result1 error
	}
	DeleteSIPTrunkStub        func(context.Context, *livekit.SIPTrunkInfo) error
	deleteSIPTrunkMutex       sync.RWMutex
	deleteSIPTrunkArgsForCall []struct {
		arg1 context.Context
		arg2 *livekit.SIPTrunkInfo
	}
	deleteSIPTrunkReturns struct {
		result1 error
	}
	deleteSIPTrunkReturnsOnCall map[int]struct {
		result1 error
	}
	ListSIPDispatchRuleStub        func(context.Context) ([]*livekit.SIPDispatchRuleInfo, error)
	listSIPDispatchRuleMutex       sync.RWMutex
	listSIPDispatchRuleArgsForCall []struct {
		arg1 context.Context
	}
	listSIPDispatchRuleReturns struct {
		result1 []*livekit.SIPDispatchRuleInfo
		result2 error
	}
	listSIPDispatchRuleReturnsOnCall map[int]struct {
		result1 []*livekit.SIPDispatchRuleInfo
		result2 error
	}
	ListSIPTrunkStub        func(context.Context) ([]*livekit.SIPTrunkInfo, error)
	listSIPTrunkMutex       sync.RWMutex
	listSIPTrunkArgsForCall []struct {
		arg1 context.Context
	}
	listSIPTrunkReturns struct {
		result1 []*livekit.SIPTrunkInfo
		result2 error
	}
	listSIPTrunkReturnsOnCall map[int]struct {
		result1 []*livekit.SIPTrunkInfo
		result2 error
	}
	LoadSIPDispatchRuleStub        func(context.Context, string) (*livekit.SIPDispatchRuleInfo, error)
	loadSIPDispatchRuleMutex       sync.RWMutex
	loadSIPDispatchRuleArgsForCall []struct {
		arg1 context.Context
		arg2 string
	}
	loadSIPDispatchRuleReturns struct {
		result1 *livekit.SIPDispatchRuleInfo
		result2 error
	}
	loadSIPDispatchRuleReturnsOnCall map[int]struct {
		result1 *livekit.SIPDispatchRuleInfo
		result2 error
	}
	LoadSIPTrunkStub        func(context.Context, string) (*livekit.SIPTrunkInfo, error)
	loadSIPTrunkMutex       sync.RWMutex
	loadSIPTrunkArgsForCall []struct {
		arg1 context.Context
		arg2 string
	}
	loadSIPTrunkReturns struct {
		result1 *livekit.SIPTrunkInfo
		result2 error
	}
	loadSIPTrunkReturnsOnCall map[int]struct {
		result1 *livekit.SIPTrunkInfo
		result2 error
	}
	StoreSIPDispatchRuleStub        func(context.Context, *livekit.SIPDispatchRuleInfo) error
	storeSIPDispatchRuleMutex       sync.RWMutex
	storeSIPDispatchRuleArgsForCall []struct {
		arg1 context.Context
		arg2 *livekit.SIPDispatchRuleInfo
	}
	storeSIPDispatchRuleReturns struct {
		result1 error
	}
	storeSIPDispatchRuleReturnsOnCall map[int]struct {
		result1 error
	}
	StoreSIPTrunkStub        func(context.Context, *livekit.SIPTrunkInfo) error
	storeSIPTrunkMutex       sync.RWMutex
	storeSIPTrunkArgsForCall []struct {
		arg1 context.Context
		arg2 *livekit.SIPTrunkInfo
	}
	storeSIPTrunkReturns struct {
		result1 error
	}
	storeSIPTrunkReturnsOnCall map[int]struct {
		result1 error
	}
	invocations      map[string][][]interface{}
	invocationsMutex sync.RWMutex
}

func (fake *FakeSIPStore) DeleteSIPDispatchRule(arg1 context.Context, arg2 *livekit.SIPDispatchRuleInfo) error {
	fake.deleteSIPDispatchRuleMutex.Lock()
	ret, specificReturn := fake.deleteSIPDispatchRuleReturnsOnCall[len(fake.deleteSIPDispatchRuleArgsForCall)]
	fake.deleteSIPDispatchRuleArgsForCall = append(fake.deleteSIPDispatchRuleArgsForCall, struct {
		arg1 context.Context
		arg2 *livekit.SIPDispatchRuleInfo
	}{arg1, arg2})
	stub := fake.DeleteSIPDispatchRuleStub
	fakeReturns := fake.deleteSIPDispatchRuleReturns
	fake.recordInvocation("DeleteSIPDispatchRule", []interface{}{arg1, arg2})
	fake.deleteSIPDispatchRuleMutex.Unlock()
	if stub != nil {
		return stub(arg1, arg2)
	}
	if specificReturn {
		return ret.result1
	}
	return fakeReturns.result1
}

func (fake *FakeSIPStore) DeleteSIPDispatchRuleCallCount() int {
	fake.deleteSIPDispatchRuleMutex.RLock()
	defer fake.deleteSIPDispatchRuleMutex.RUnlock()
	return len(fake.deleteSIPDispatchRuleArgsForCall)
}

func (fake *FakeSIPStore) DeleteSIPDispatchRuleCalls(stub func(context.Context, *livekit.SIPDispatchRuleInfo) error) {
	fake.deleteSIPDispatchRuleMutex.Lock()
	defer fake.deleteSIPDispatchRuleMutex.Unlock()
	fake.DeleteSIPDispatchRuleStub = stub
}

func (fake *FakeSIPStore) DeleteSIPDispatchRuleArgsForCall(i int) (context.Context, *livekit.SIPDispatchRuleInfo) {
	fake.deleteSIPDispatchRuleMutex.RLock()
	defer fake.deleteSIPDispatchRuleMutex.RUnlock()
	argsForCall := fake.deleteSIPDispatchRuleArgsForCall[i]
	return argsForCall.arg1, argsForCall.arg2
}

func (fake *FakeSIPStore) DeleteSIPDispatchRuleReturns(result1 error) {
	fake.deleteSIPDispatchRuleMutex.Lock()
	defer fake.deleteSIPDispatchRuleMutex.Unlock()
	fake.DeleteSIPDispatchRuleStub = nil
	fake.deleteSIPDispatchRuleReturns = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) DeleteSIPDispatchRuleReturnsOnCall(i int, result1 error) {
	fake.deleteSIPDispatchRuleMutex.Lock()
	defer fake.deleteSIPDispatchRuleMutex.Unlock()
	fake.DeleteSIPDispatchRuleStub = nil
	if fake.deleteSIPDispatchRuleReturnsOnCall == nil {
		fake.deleteSIPDispatchRuleReturnsOnCall = make(map[int]struct {
			result1 error
		})
	}
	fake.deleteSIPDispatchRuleReturnsOnCall[i] = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) DeleteSIPTrunk(arg1 context.Context, arg2 *livekit.SIPTrunkInfo) error {
	fake.deleteSIPTrunkMutex.Lock()
	ret, specificReturn := fake.deleteSIPTrunkReturnsOnCall[len(fake.deleteSIPTrunkArgsForCall)]
	fake.deleteSIPTrunkArgsForCall = append(fake.deleteSIPTrunkArgsForCall, struct {
		arg1 context.Context
		arg2 *livekit.SIPTrunkInfo
	}{arg1, arg2})
	stub := fake.DeleteSIPTrunkStub
	fakeReturns := fake.deleteSIPTrunkReturns
	fake.recordInvocation("DeleteSIPTrunk", []interface{}{arg1, arg2})
	fake.deleteSIPTrunkMutex.Unlock()
	if stub != nil {
		return stub(arg1, arg2)
	}
	if specificReturn {
		return ret.result1
	}
	return fakeReturns.result1
}

func (fake *FakeSIPStore) DeleteSIPTrunkCallCount() int {
	fake.deleteSIPTrunkMutex.RLock()
	defer fake.deleteSIPTrunkMutex.RUnlock()
	return len(fake.deleteSIPTrunkArgsForCall)
}

func (fake *FakeSIPStore) DeleteSIPTrunkCalls(stub func(context.Context, *livekit.SIPTrunkInfo) error) {
	fake.deleteSIPTrunkMutex.Lock()
	defer fake.deleteSIPTrunkMutex.Unlock()
	fake.DeleteSIPTrunkStub = stub
}

func (fake *FakeSIPStore) DeleteSIPTrunkArgsForCall(i int) (context.Context, *livekit.SIPTrunkInfo) {
	fake.deleteSIPTrunkMutex.RLock()
	defer fake.deleteSIPTrunkMutex.RUnlock()
	argsForCall := fake.deleteSIPTrunkArgsForCall[i]
	return argsForCall.arg1, argsForCall.arg2
}

func (fake *FakeSIPStore) DeleteSIPTrunkReturns(result1 error) {
	fake.deleteSIPTrunkMutex.Lock()
	defer fake.deleteSIPTrunkMutex.Unlock()
	fake.DeleteSIPTrunkStub = nil
	fake.deleteSIPTrunkReturns = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) DeleteSIPTrunkReturnsOnCall(i int, result1 error) {
	fake.deleteSIPTrunkMutex.Lock()
	defer fake.deleteSIPTrunkMutex.Unlock()
	fake.DeleteSIPTrunkStub = nil
	if fake.deleteSIPTrunkReturnsOnCall == nil {
		fake.deleteSIPTrunkReturnsOnCall = make(map[int]struct {
			result1 error
		})
	}
	fake.deleteSIPTrunkReturnsOnCall[i] = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) ListSIPDispatchRule(arg1 context.Context) ([]*livekit.SIPDispatchRuleInfo, error) {
	fake.listSIPDispatchRuleMutex.Lock()
	ret, specificReturn := fake.listSIPDispatchRuleReturnsOnCall[len(fake.listSIPDispatchRuleArgsForCall)]
	fake.listSIPDispatchRuleArgsForCall = append(fake.listSIPDispatchRuleArgsForCall, struct {
		arg1 context.Context
	}{arg1})
	stub := fake.ListSIPDispatchRuleStub
	fakeReturns := fake.listSIPDispatchRuleReturns
	fake.recordInvocation("ListSIPDispatchRule", []interface{}{arg1})
	fake.listSIPDispatchRuleMutex.Unlock()
	if stub != nil {
		return stub(arg1)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeSIPStore) ListSIPDispatchRuleCallCount() int {
	fake.listSIPDispatchRuleMutex.RLock()
	defer fake.listSIPDispatchRuleMutex.RUnlock()
	return len(fake.listSIPDispatchRuleArgsForCall)
}

func (fake *FakeSIPStore) ListSIPDispatchRuleCalls(stub func(context.Context) ([]*livekit.SIPDispatchRuleInfo, error)) {
	fake.listSIPDispatchRuleMutex.Lock()
	defer fake.listSIPDispatchRuleMutex.Unlock()
	fake.ListSIPDispatchRuleStub = stub
}

func (fake *FakeSIPStore) ListSIPDispatchRuleArgsForCall(i int) context.Context {
	fake.listSIPDispatchRuleMutex.RLock()
	defer fake.listSIPDispatchRuleMutex.RUnlock()
	argsForCall := fake.listSIPDispatchRuleArgsForCall[i]
	return argsForCall.arg1
}

func (fake *FakeSIPStore) ListSIPDispatchRuleReturns(result1 []*livekit.SIPDispatchRuleInfo, result2 error) {
	fake.listSIPDispatchRuleMutex.Lock()
	defer fake.listSIPDispatchRuleMutex.Unlock()
	fake.ListSIPDispatchRuleStub = nil
	fake.listSIPDispatchRuleReturns = struct {
		result1 []*livekit.SIPDispatchRuleInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) ListSIPDispatchRuleReturnsOnCall(i int, result1 []*livekit.SIPDispatchRuleInfo, result2 error) {
	fake.listSIPDispatchRuleMutex.Lock()
	defer fake.listSIPDispatchRuleMutex.Unlock()
	fake.ListSIPDispatchRuleStub = nil
	if fake.listSIPDispatchRuleReturnsOnCall == nil {
		fake.listSIPDispatchRuleReturnsOnCall = make(map[int]struct {
			result1 []*livekit.SIPDispatchRuleInfo
			result2 error
		})
	}
	fake.listSIPDispatchRuleReturnsOnCall[i] = struct {
		result1 []*livekit.SIPDispatchRuleInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) ListSIPTrunk(arg1 context.Context) ([]*livekit.SIPTrunkInfo, error) {
	fake.listSIPTrunkMutex.Lock()
	ret, specificReturn := fake.listSIPTrunkReturnsOnCall[len(fake.listSIPTrunkArgsForCall)]
	fake.listSIPTrunkArgsForCall = append(fake.listSIPTrunkArgsForCall, struct {
		arg1 context.Context
	}{arg1})
	stub := fake.ListSIPTrunkStub
	fakeReturns := fake.listSIPTrunkReturns
	fake.recordInvocation("ListSIPTrunk", []interface{}{arg1})
	fake.listSIPTrunkMutex.Unlock()
	if stub != nil {
		return stub(arg1)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeSIPStore) ListSIPTrunkCallCount() int {
	fake.listSIPTrunkMutex.RLock()
	defer fake.listSIPTrunkMutex.RUnlock()
	return len(fake.listSIPTrunkArgsForCall)
}

func (fake *FakeSIPStore) ListSIPTrunkCalls(stub func(context.Context) ([]*livekit.SIPTrunkInfo, error)) {
	fake.listSIPTrunkMutex.Lock()
	defer fake.listSIPTrunkMutex.Unlock()
	fake.ListSIPTrunkStub = stub
}

func (fake *FakeSIPStore) ListSIPTrunkArgsForCall(i int) context.Context {
	fake.listSIPTrunkMutex.RLock()
	defer fake.listSIPTrunkMutex.RUnlock()
	argsForCall := fake.listSIPTrunkArgsForCall[i]
	return argsForCall.arg1
}

func (fake *FakeSIPStore) ListSIPTrunkReturns(result1 []*livekit.SIPTrunkInfo, result2 error) {
	fake.listSIPTrunkMutex.Lock()
	defer fake.listSIPTrunkMutex.Unlock()
	fake.ListSIPTrunkStub = nil
	fake.listSIPTrunkReturns = struct {
		result1 []*livekit.SIPTrunkInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) ListSIPTrunkReturnsOnCall(i int, result1 []*livekit.SIPTrunkInfo, result2 error) {
	fake.listSIPTrunkMutex.Lock()
	defer fake.listSIPTrunkMutex.Unlock()
	fake.ListSIPTrunkStub = nil
	if fake.listSIPTrunkReturnsOnCall == nil {
		fake.listSIPTrunkReturnsOnCall = make(map[int]struct {
			result1 []*livekit.SIPTrunkInfo
			result2 error
		})
	}
	fake.listSIPTrunkReturnsOnCall[i] = struct {
		result1 []*livekit.SIPTrunkInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) LoadSIPDispatchRule(arg1 context.Context, arg2 string) (*livekit.SIPDispatchRuleInfo, error) {
	fake.loadSIPDispatchRuleMutex.Lock()
	ret, specificReturn := fake.loadSIPDispatchRuleReturnsOnCall[len(fake.loadSIPDispatchRuleArgsForCall)]
	fake.loadSIPDispatchRuleArgsForCall = append(fake.loadSIPDispatchRuleArgsForCall, struct {
		arg1 context.Context
		arg2 string
	}{arg1, arg2})
	stub := fake.LoadSIPDispatchRuleStub
	fakeReturns := fake.loadSIPDispatchRuleReturns
	fake.recordInvocation("LoadSIPDispatchRule", []interface{}{arg1, arg2})
	fake.loadSIPDispatchRuleMutex.Unlock()
	if stub != nil {
		return stub(arg1, arg2)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeSIPStore) LoadSIPDispatchRuleCallCount() int {
	fake.loadSIPDispatchRuleMutex.RLock()
	defer fake.loadSIPDispatchRuleMutex.RUnlock()
	return len(fake.loadSIPDispatchRuleArgsForCall)
}

func (fake *FakeSIPStore) LoadSIPDispatchRuleCalls(stub func(context.Context, string) (*livekit.SIPDispatchRuleInfo, error)) {
	fake.loadSIPDispatchRuleMutex.Lock()
	defer fake.loadSIPDispatchRuleMutex.Unlock()
	fake.LoadSIPDispatchRuleStub = stub
}

func (fake *FakeSIPStore) LoadSIPDispatchRuleArgsForCall(i int) (context.Context, string) {
	fake.loadSIPDispatchRuleMutex.RLock()
	defer fake.loadSIPDispatchRuleMutex.RUnlock()
	argsForCall := fake.loadSIPDispatchRuleArgsForCall[i]
	return argsForCall.arg1, argsForCall.arg2
}

func (fake *FakeSIPStore) LoadSIPDispatchRuleReturns(result1 *livekit.SIPDispatchRuleInfo, result2 error) {
	fake.loadSIPDispatchRuleMutex.Lock()
	defer fake.loadSIPDispatchRuleMutex.Unlock()
	fake.LoadSIPDispatchRuleStub = nil
	fake.loadSIPDispatchRuleReturns = struct {
		result1 *livekit.SIPDispatchRuleInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) LoadSIPDispatchRuleReturnsOnCall(i int, result1 *livekit.SIPDispatchRuleInfo, result2 error) {
	fake.loadSIPDispatchRuleMutex.Lock()
	defer fake.loadSIPDispatchRuleMutex.Unlock()
	fake.LoadSIPDispatchRuleStub = nil
	if fake.loadSIPDispatchRuleReturnsOnCall == nil {
		fake.loadSIPDispatchRuleReturnsOnCall = make(map[int]struct {
			result1 *livekit.SIPDispatchRuleInfo
			result2 error
		})
	}
	fake.loadSIPDispatchRuleReturnsOnCall[i] = struct {
		result1 *livekit.SIPDispatchRuleInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) LoadSIPTrunk(arg1 context.Context, arg2 string) (*livekit.SIPTrunkInfo, error) {
	fake.loadSIPTrunkMutex.Lock()
	ret, specificReturn := fake.loadSIPTrunkReturnsOnCall[len(fake.loadSIPTrunkArgsForCall)]
	fake.loadSIPTrunkArgsForCall = append(fake.loadSIPTrunkArgsForCall, struct {
		arg1 context.Context
		arg2 string
	}{arg1, arg2})
	stub := fake.LoadSIPTrunkStub
	fakeReturns := fake.loadSIPTrunkReturns
	fake.recordInvocation("LoadSIPTrunk", []interface{}{arg1, arg2})
	fake.loadSIPTrunkMutex.Unlock()
	if stub != nil {
		return stub(arg1, arg2)
	}
	if specificReturn {
		return ret.result1, ret.result2
	}
	return fakeReturns.result1, fakeReturns.result2
}

func (fake *FakeSIPStore) LoadSIPTrunkCallCount() int {
	fake.loadSIPTrunkMutex.RLock()
	defer fake.loadSIPTrunkMutex.RUnlock()
	return len(fake.loadSIPTrunkArgsForCall)
}

func (fake *FakeSIPStore) LoadSIPTrunkCalls(stub func(context.Context, string) (*livekit.SIPTrunkInfo, error)) {
	fake.loadSIPTrunkMutex.Lock()
	defer fake.loadSIPTrunkMutex.Unlock()
	fake.LoadSIPTrunkStub = stub
}

func (fake *FakeSIPStore) LoadSIPTrunkArgsForCall(i int) (context.Context, string) {
	fake.loadSIPTrunkMutex.RLock()
	defer fake.loadSIPTrunkMutex.RUnlock()
	argsForCall := fake.loadSIPTrunkArgsForCall[i]
	return argsForCall.arg1, argsForCall.arg2
}

func (fake *FakeSIPStore) LoadSIPTrunkReturns(result1 *livekit.SIPTrunkInfo, result2 error) {
	fake.loadSIPTrunkMutex.Lock()
	defer fake.loadSIPTrunkMutex.Unlock()
	fake.LoadSIPTrunkStub = nil
	fake.loadSIPTrunkReturns = struct {
		result1 *livekit.SIPTrunkInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) LoadSIPTrunkReturnsOnCall(i int, result1 *livekit.SIPTrunkInfo, result2 error) {
	fake.loadSIPTrunkMutex.Lock()
	defer fake.loadSIPTrunkMutex.Unlock()
	fake.LoadSIPTrunkStub = nil
	if fake.loadSIPTrunkReturnsOnCall == nil {
		fake.loadSIPTrunkReturnsOnCall = make(map[int]struct {
			result1 *livekit.SIPTrunkInfo
			result2 error
		})
	}
	fake.loadSIPTrunkReturnsOnCall[i] = struct {
		result1 *livekit.SIPTrunkInfo
		result2 error
	}{result1, result2}
}

func (fake *FakeSIPStore) StoreSIPDispatchRule(arg1 context.Context, arg2 *livekit.SIPDispatchRuleInfo) error {
	fake.storeSIPDispatchRuleMutex.Lock()
	ret, specificReturn := fake.storeSIPDispatchRuleReturnsOnCall[len(fake.storeSIPDispatchRuleArgsForCall)]
	fake.storeSIPDispatchRuleArgsForCall = append(fake.storeSIPDispatchRuleArgsForCall, struct {
		arg1 context.Context
		arg2 *livekit.SIPDispatchRuleInfo
	}{arg1, arg2})
	stub := fake.StoreSIPDispatchRuleStub
	fakeReturns := fake.storeSIPDispatchRuleReturns
	fake.recordInvocation("StoreSIPDispatchRule", []interface{}{arg1, arg2})
	fake.storeSIPDispatchRuleMutex.Unlock()
	if stub != nil {
		return stub(arg1, arg2)
	}
	if specificReturn {
		return ret.result1
	}
	return fakeReturns.result1
}

func (fake *FakeSIPStore) StoreSIPDispatchRuleCallCount() int {
	fake.storeSIPDispatchRuleMutex.RLock()
	defer fake.storeSIPDispatchRuleMutex.RUnlock()
	return len(fake.storeSIPDispatchRuleArgsForCall)
}

func (fake *FakeSIPStore) StoreSIPDispatchRuleCalls(stub func(context.Context, *livekit.SIPDispatchRuleInfo) error) {
	fake.storeSIPDispatchRuleMutex.Lock()
	defer fake.storeSIPDispatchRuleMutex.Unlock()
	fake.StoreSIPDispatchRuleStub = stub
}

func (fake *FakeSIPStore) StoreSIPDispatchRuleArgsForCall(i int) (context.Context, *livekit.SIPDispatchRuleInfo) {
	fake.storeSIPDispatchRuleMutex.RLock()
	defer fake.storeSIPDispatchRuleMutex.RUnlock()
	argsForCall := fake.storeSIPDispatchRuleArgsForCall[i]
	return argsForCall.arg1, argsForCall.arg2
}

func (fake *FakeSIPStore) StoreSIPDispatchRuleReturns(result1 error) {
	fake.storeSIPDispatchRuleMutex.Lock()
	defer fake.storeSIPDispatchRuleMutex.Unlock()
	fake.StoreSIPDispatchRuleStub = nil
	fake.storeSIPDispatchRuleReturns = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) StoreSIPDispatchRuleReturnsOnCall(i int, result1 error) {
	fake.storeSIPDispatchRuleMutex.Lock()
	defer fake.storeSIPDispatchRuleMutex.Unlock()
	fake.StoreSIPDispatchRuleStub = nil
	if fake.storeSIPDispatchRuleReturnsOnCall == nil {
		fake.storeSIPDispatchRuleReturnsOnCall = make(map[int]struct {
			result1 error
		})
	}
	fake.storeSIPDispatchRuleReturnsOnCall[i] = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) StoreSIPTrunk(arg1 context.Context, arg2 *livekit.SIPTrunkInfo) error {
	fake.storeSIPTrunkMutex.Lock()
	ret, specificReturn := fake.storeSIPTrunkReturnsOnCall[len(fake.storeSIPTrunkArgsForCall)]
	fake.storeSIPTrunkArgsForCall = append(fake.storeSIPTrunkArgsForCall, struct {
		arg1 context.Context
		arg2 *livekit.SIPTrunkInfo
	}{arg1, arg2})
	stub := fake.StoreSIPTrunkStub
	fakeReturns := fake.storeSIPTrunkReturns
	fake.recordInvocation("StoreSIPTrunk", []interface{}{arg1, arg2})
	fake.storeSIPTrunkMutex.Unlock()
	if stub != nil {
		return stub(arg1, arg2)
	}
	if specificReturn {
		return ret.result1
	}
	return fakeReturns.result1
}

func (fake *FakeSIPStore) StoreSIPTrunkCallCount() int {
	fake.storeSIPTrunkMutex.RLock()
	defer fake.storeSIPTrunkMutex.RUnlock()
	return len(fake.storeSIPTrunkArgsForCall)
}

func (fake *FakeSIPStore) StoreSIPTrunkCalls(stub func(context.Context, *livekit.SIPTrunkInfo) error) {
	fake.storeSIPTrunkMutex.Lock()
	defer fake.storeSIPTrunkMutex.Unlock()
	fake.StoreSIPTrunkStub = stub
}

func (fake *FakeSIPStore) StoreSIPTrunkArgsForCall(i int) (context.Context, *livekit.SIPTrunkInfo) {
	fake.storeSIPTrunkMutex.RLock()
	defer fake.storeSIPTrunkMutex.RUnlock()
	argsForCall := fake.storeSIPTrunkArgsForCall[i]
	return argsForCall.arg1, argsForCall.arg2
}

func (fake *FakeSIPStore) StoreSIPTrunkReturns(result1 error) {
	fake.storeSIPTrunkMutex.Lock()
	defer fake.storeSIPTrunkMutex.Unlock()
	fake.StoreSIPTrunkStub = nil
	fake.storeSIPTrunkReturns = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) StoreSIPTrunkReturnsOnCall(i int, result1 error) {
	fake.storeSIPTrunkMutex.Lock()
	defer fake.storeSIPTrunkMutex.Unlock()
	fake.StoreSIPTrunkStub = nil
	if fake.storeSIPTrunkReturnsOnCall == nil {
		fake.storeSIPTrunkReturnsOnCall = make(map[int]struct {
			result1 error
		})
	}
	fake.storeSIPTrunkReturnsOnCall[i] = struct {
		result1 error
	}{result1}
}

func (fake *FakeSIPStore) Invocations() map[string][][]interface{} {
	fake.invocationsMutex.RLock()
	defer fake.invocationsMutex.RUnlock()
	fake.deleteSIPDispatchRuleMutex.RLock()
	defer fake.deleteSIPDispatchRuleMutex.RUnlock()
	fake.deleteSIPTrunkMutex.RLock()
	defer fake.deleteSIPTrunkMutex.RUnlock()
	fake.listSIPDispatchRuleMutex.RLock()
	defer fake.listSIPDispatchRuleMutex.RUnlock()
	fake.listSIPTrunkMutex.RLock()
	defer fake.listSIPTrunkMutex.RUnlock()
	fake.loadSIPDispatchRuleMutex.RLock()
	defer fake.loadSIPDispatchRuleMutex.RUnlock()
	fake.loadSIPTrunkMutex.RLock()
	defer fake.loadSIPTrunkMutex.RUnlock()
	fake.storeSIPDispatchRuleMutex.RLock()
	defer fake.storeSIPDispatchRuleMutex.RUnlock()
	fake.storeSIPTrunkMutex.RLock()
	defer fake.storeSIPTrunkMutex.RUnlock()
	copiedInvocations := map[string][][]interface{}{}
	for key, value := range fake.invocations {
		copiedInvocations[key] = value
	}
	return copiedInvocations
}

func (fake *FakeSIPStore) recordInvocation(key string, args []interface{}) {
	fake.invocationsMutex.Lock()
	defer fake.invocationsMutex.Unlock()
	if fake.invocations == nil {
		fake.invocations = map[string][][]interface{}{}
	}
	if fake.invocations[key] == nil {
		fake.invocations[key] = [][]interface{}{}
	}
	fake.invocations[key] = append(fake.invocations[key], args)
}

var _ service.SIPStore = new(FakeSIPStore)
