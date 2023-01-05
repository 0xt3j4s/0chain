package partitions

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"

	"0chain.net/core/datastore"
	"0chain.net/core/encryption"
	"github.com/0chain/common/core/logging"
	"github.com/0chain/common/core/util"
	"go.uber.org/zap"

	"0chain.net/chaincore/chain/state"
)

const notFoundIndex = -1

//msgp:ignore randomSelector
//go:generate msgp -io=false -tests=false -unexported=true -v

type randomSelector struct {
	Name          string       `json:"name"`
	PartitionSize int          `json:"partition_size"`
	NumPartitions int          `json:"num_partitions"`
	Partitions    []*partition `json:"-" msg:"-"`
}

func newRandomSelector(
	name string,
	size int,
) (*randomSelector, error) {
	// TODO: limit the name length
	return &randomSelector{
		Name:          name,
		PartitionSize: size,
	}, nil
}

func PartitionKey(name string, index int) datastore.Key {
	return name + encryption.Hash(":partition:"+strconv.Itoa(index))
}

func (rs *randomSelector) partitionKey(index int) datastore.Key {
	return PartitionKey(rs.Name, index)
}

func (rs *randomSelector) Add(state state.StateContextI, item PartitionItem) (int, error) {
	var part *partition
	var err error
	if rs.partitionsNum() > 0 {
		part, err = rs.getPartition(state, rs.partitionsNum()-1)
		if err != nil {
			return 0, err
		}
	}
	if rs.partitionsNum() == 0 || part.length() >= rs.PartitionSize {
		part = rs.addPartition()
	}
	if err := part.add(item); err != nil {
		return 0, err
	}
	return rs.partitionsNum() - 1, nil
}

func (rs *randomSelector) RemoveItem(
	state state.StateContextI,
	id string,
	index int,
) error {
	part, err := rs.getPartition(state, index)
	if err != nil {
		return err
	}

	err = part.remove(id)
	if err != nil {
		return err
	}

	lastPart, err := rs.getPartition(state, rs.partitionsNum()-1)
	if err != nil {
		return err
	}

	if index == rs.partitionsNum()-1 {
		if lastPart.length() == 0 {
			if err := rs.deleteTail(state); err != nil {
				return err
			}
		}
		return nil
	}

	replace := lastPart.cutTail()
	if replace == nil {
		logging.Logger.Error("empty last partition - should not happen!!",
			zap.Int("part index", rs.NumPartitions-1),
			zap.Int("part num", rs.NumPartitions),
			zap.Int("parts len", rs.NumPartitions))

		return fmt.Errorf("empty last partitions, currpt data")
	}
	if err := part.addRaw(*replace); err != nil {
		return err
	}

	if lastPart.length() == 0 {
		if err := rs.deleteTail(state); err != nil {
			return err
		}
	}

	return nil
}

func (rs *randomSelector) GetRandomItems(state state.StateContextI, r *rand.Rand, vs interface{}) error {
	if rs.partitionsNum() == 0 {
		return errors.New("empty list, no items to return")
	}
	index := r.Intn(rs.partitionsNum())

	part, err := rs.getPartition(state, index)
	if err != nil {
		return err
	}

	its, err := part.itemRange(0, part.length())
	if err != nil {
		return err
	}

	rtv := make([]item, 0, rs.PartitionSize)
	rtv = append(rtv, its...)

	if index == rs.partitionsNum()-1 && len(rtv) < rs.PartitionSize && rs.partitionsNum() > 1 {
		secondLast, err := rs.getPartition(state, index-1)
		if err != nil {
			return err
		}
		want := rs.PartitionSize - len(rtv)
		if secondLast.length() < want {
			return fmt.Errorf("second last part too small %d instead of %d",
				secondLast.length(), rs.partitionsNum())
		}
		its, err := secondLast.itemRange(secondLast.length()-want, secondLast.length())
		if err != nil {
			return err
		}

		rtv = append(rtv, its...)
	}

	return setPartitionItems(rtv, vs)
}

// UpdateRandomItems similar to GetRandomItems() but the changes will be saved
func (rs *randomSelector) UpdateRandomItems(state state.StateContextI, r *rand.Rand, randN int,
	f func(key string, data []byte) ([]byte, error)) error {
	if rs.partitionsNum() == 0 {
		return nil
	}

	if randN > rs.PartitionSize {
		return errors.New("randN can not be greater than partition size")
	}

	index := r.Intn(rs.partitionsNum())
	part, err := rs.getPartition(state, index)
	if err != nil {
		return err
	}

	var secondLast *partition
	if index == rs.partitionsNum()-1 && len(part.Items) < rs.PartitionSize && rs.partitionsNum() > 1 {
		var err error
		secondLast, err = rs.getPartition(state, index-1)
		if err != nil {
			return err
		}
		want := rs.PartitionSize - len(part.Items)
		if secondLast.length() < want {
			return fmt.Errorf("second last part too small %d instead of %d",
				secondLast.length(), rs.partitionsNum())
		}
	}

	if secondLast != nil {
		num := part.length() + secondLast.length()
		if num < randN {
			randN = num
		}
	}

	for _, i := range rand.Perm(randN) {
		if i < part.length() {
			nData, err := f(part.Items[i].ID, part.Items[i].Data)
			if err != nil {
				return err
			}

			part.Items[i].Data = nData
			part.Changed = true
		} else {
			nData, err := f(secondLast.Items[i].ID, secondLast.Items[i].Data)
			if err != nil {
				return err
			}

			secondLast.Items[i].Data = nData
			secondLast.Changed = true
		}
	}

	return nil
}

func (rs *randomSelector) foreach(state state.StateContextI, f func(string, []byte, int) ([]byte, bool, error)) error {
	for i := 0; i < rs.partitionsNum(); i++ {
		part, err := rs.getPartition(state, i)
		if err != nil {
			return fmt.Errorf("could not get partition: name:%s, index: %d", rs.Name, i)
		}

		for i, v := range part.Items {
			ret, bk, err := f(v.ID, v.Data, i)
			if err != nil {
				return err
			}
			if !bytes.Equal(ret, v.Data) {
				v.Data = ret
				part.Items[i] = v
				part.Changed = true
			}

			if bk {
				return nil
			}
		}
	}

	return nil
}

func setPartitionItems(rtv []item, vs interface{}) error {
	// slice type
	vst := reflect.TypeOf(vs)
	if vst.Kind() != reflect.Ptr {
		return errors.New("invalid return value type, it must be a pointer of slice")
	}

	// element type - slice
	vts := vst.Elem()
	if vts.Kind() != reflect.Slice {
		return errors.New("invalid return value type, it must be a pointer of slice")
	}

	// item type
	vt := vts.Elem()

	// create a new item slice
	rv := reflect.MakeSlice(vts, len(rtv), len(rtv))

	for i, v := range rtv {
		// create new item instance and assert PartitionItem interface
		pi, ok := reflect.New(vt).Interface().(PartitionItem)
		if !ok {
			return errors.New("invalid value type, the item does not meet PartitionItem interface")
		}

		// decode data
		if _, err := pi.UnmarshalMsg(v.Data); err != nil {
			return err
		}

		// set to slice
		rv.Index(i).Set(reflect.ValueOf(pi).Elem())
	}

	// set slice back to v param
	reflect.ValueOf(vs).Elem().Set(rv)
	return nil
}

func (rs *randomSelector) addPartition() *partition {
	newPartition := &partition{
		Key: rs.partitionKey(rs.partitionsNum()),
	}

	rs.Partitions = append(rs.Partitions, newPartition)
	rs.NumPartitions++
	return newPartition
}

func (rs *randomSelector) deleteTail(balances state.StateContextI) error {
	_, err := balances.DeleteTrieNode(rs.partitionKey(rs.partitionsNum() - 1))
	if err != nil {
		if err != util.ErrValueNotPresent {
			return err
		}
	}
	rs.Partitions = rs.Partitions[:rs.partitionsNum()-1]
	rs.NumPartitions--
	return nil
}

func (rs *randomSelector) Size(state state.StateContextI) (int, error) {
	if rs.partitionsNum() == 0 {
		return 0, nil
	}
	lastPartition, err := rs.getPartition(state, rs.partitionsNum()-1)
	if err != nil {
		return 0, err
	}

	return (rs.partitionsNum()-1)*rs.PartitionSize + lastPartition.length(), nil
}

func (rs *randomSelector) Save(balances state.StateContextI) error {
	for _, partition := range rs.Partitions {
		if partition != nil && partition.changed() {
			err := partition.save(balances)
			if err != nil {
				return err
			}
		}
	}

	_, err := balances.InsertTrieNode(rs.Name, rs)
	if err != nil {
		return err
	}
	return nil
}

func (rs *randomSelector) getPartition(state state.StateContextI, i int) (*partition, error) {
	if i >= rs.partitionsNum() {
		return nil, fmt.Errorf("partition id %v greater than number of partitions %v", i, rs.partitionsNum())
	}
	if rs.Partitions[i] != nil {
		return rs.Partitions[i], nil
	}

	part := &partition{}
	err := part.load(state, rs.partitionKey(i))
	if err != nil {
		return nil, err
	}
	rs.Partitions[i] = part
	return part, nil
}

func (rs *randomSelector) partitionsNum() int {
	// assert the partitions number match
	if rs.NumPartitions != len(rs.Partitions) {
		logging.Logger.DPanic(fmt.Sprintf("number of partitions mismatch, numPartitions: %d, len(partitions): %d",
			rs.NumPartitions, len(rs.Partitions)))
	}
	return rs.NumPartitions
}

func (rs *randomSelector) MarshalMsg(o []byte) ([]byte, error) {
	d := randomSelectorDecode(*rs)
	return d.MarshalMsg(o)
}

func (rs *randomSelector) UnmarshalMsg(b []byte) ([]byte, error) {
	d := &randomSelectorDecode{}
	o, err := d.UnmarshalMsg(b)
	if err != nil {
		return nil, err
	}

	*rs = randomSelector(*d)

	rs.Partitions = make([]*partition, d.NumPartitions)
	return o, nil
}

func (rs *randomSelector) Msgsize() int {
	d := randomSelectorDecode(*rs)
	return d.Msgsize()
}

type randomSelectorDecode randomSelector

func (rs *randomSelector) UpdateItem(
	state state.StateContextI,
	partIndex int,
	it PartitionItem,
) error {

	partition, err := rs.getPartition(state, partIndex)
	if err != nil {
		return err
	}

	return partition.update(it)
}

func (rs *randomSelector) GetItem(
	state state.StateContextI,
	partIndex int,
	id string,
	v PartitionItem,
) error {

	pt, err := rs.getPartition(state, partIndex)
	if err != nil {
		return err
	}

	item, _, ok := pt.find(id)
	if !ok {
		return errors.New("item not present")
	}

	_, err = v.UnmarshalMsg(item.Data)
	return err
}
