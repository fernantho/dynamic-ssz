// dynssz: Dynamic SSZ encoding/decoding for Ethereum with fastssz efficiency.
// This file is part of the dynssz package.
// Copyright (c) 2024 by pk910. Refer to LICENSE for more information.
package dynssz

import (
	"fmt"
	"reflect"
	"strings"
)

func (d *DynSsz) buildRootFromType(sourceType reflect.Type, sourceValue reflect.Value, hh *Hasher, sizeHints []sszSizeHint, maxSizeHints []sszMaxSizeHint, idt int) error {
	hashIndex := hh.Index()

	if sourceType.Kind() == reflect.Ptr {
		sourceType = sourceType.Elem()
		sourceValue = sourceValue.Elem()
	}

	// use fastssz to hash types if:
	// - type implements fastssz HashRoot interface
	// - this type or any child type does not use spec specific field sizes
	fastsszCompat, err := d.getFastsszHashCompatibility(sourceType, sizeHints, maxSizeHints)
	if err != nil {
		return fmt.Errorf("failed checking fastssz compatibility: %v", err)
	}

	useFastSsz := !d.NoFastSsz && fastsszCompat.isHashRoot && !fastsszCompat.hasDynamicSpecSizes && !fastsszCompat.hasDynamicSpecMax
	if !useFastSsz && fastsszCompat.isHashRoot && !fastsszCompat.hasDynamicSpecSizes && !fastsszCompat.hasDynamicSpecMax && sourceType.Name() == "Int" {
		// hack for uint256.Int
		useFastSsz = true
	}

	if d.Verbose {
		fmt.Printf("%stype: %s\t kind: %v\t fastssz: %v (compat: %v/ dynamic: %v/%v)\t index: %v\n", strings.Repeat(" ", idt), sourceType.Name(), sourceType.Kind(), useFastSsz, fastsszCompat.isHashRoot, fastsszCompat.hasDynamicSpecSizes, fastsszCompat.hasDynamicSpecMax, hashIndex)
	}

	if useFastSsz {
		if hasher, ok := sourceValue.Addr().Interface().(fastsszHashRoot); ok {
			hashBytes, err := hasher.HashTreeRoot()
			if err != nil {
				return fmt.Errorf("failed HashTreeRoot: %v", err)
			}

			hh.PutBytes(hashBytes[:])
		} else {
			useFastSsz = false
		}
	}

	if !useFastSsz {
		if strings.Contains(sourceType.Name(), "Bitlist") {
			// hack for bitlists
			maxSize := uint64(0)
			bytes := sourceValue.Bytes()
			if len(maxSizeHints) > 0 {
				maxSize = maxSizeHints[0].size
			} else {
				maxSize = uint64(len(bytes) * 8)
			}

			hh.PutBitlist(bytes, maxSize)
		} else {

			switch sourceType.Kind() {
			case reflect.Struct:
				err := d.buildRootFromStruct(sourceType, sourceValue, hh, idt)
				if err != nil {
					return err
				}
			case reflect.Array:
				err := d.buildRootFromSlice(sourceType, sourceValue, hh, maxSizeHints, true, idt)
				if err != nil {
					return err
				}

			case reflect.Slice:
				err := d.buildRootFromSlice(sourceType, sourceValue, hh, maxSizeHints, false, idt)
				if err != nil {
					return err
				}

			case reflect.Bool:
				hh.PutBool(sourceValue.Bool())
			case reflect.Uint8:
				hh.PutUint8(uint8(sourceValue.Uint()))
			case reflect.Uint16:
				hh.PutUint16(uint16(sourceValue.Uint()))
			case reflect.Uint32:
				hh.PutUint32(uint32(sourceValue.Uint()))
			case reflect.Uint64:
				hh.PutUint64(uint64(sourceValue.Uint()))
			default:
				return fmt.Errorf("unknown type: %v", sourceType)
			}
		}
	}

	if d.Verbose {
		fmt.Printf("%shash: 0x%x\n", strings.Repeat(" ", idt), hh.Hash())
	}

	return nil
}

func (d *DynSsz) buildRootFromStruct(sourceType reflect.Type, sourceValue reflect.Value, hh *Hasher, idt int) error {
	hashIndex := hh.Index()

	if sourceType.Kind() == reflect.Ptr {
		sourceType = sourceType.Elem()
		sourceValue = sourceValue.Elem()
	}

	for i := 0; i < sourceType.NumField(); i++ {
		field := sourceType.Field(i)
		fieldType := field.Type
		fieldValue := sourceValue.Field(i)

		fieldIsPtr := fieldType.Kind() == reflect.Ptr
		if fieldIsPtr {
			fieldType = fieldType.Elem()
			fieldValue = fieldValue.Elem()
		}

		_, _, sizeHints, err := d.getSszFieldSize(&field)
		if err != nil {
			return err
		}
		maxSizeHints, err := d.getSszMaxSizeTag(&field)
		if err != nil {
			return err
		}

		if d.Verbose {
			fmt.Printf("%vfield %v\n", strings.Repeat(" ", idt), field.Name)
		}

		err = d.buildRootFromType(fieldType, fieldValue, hh, sizeHints, maxSizeHints, idt+2)
		if err != nil {
			return err
		}
	}
	hh.Merkleize(hashIndex)

	return nil
}

func (d *DynSsz) buildRootFromSlice(sourceType reflect.Type, sourceValue reflect.Value, hh *Hasher, maxSizeHints []sszMaxSizeHint, isArray bool, idt int) error {
	fieldType := sourceType.Elem()
	fieldIsPtr := fieldType.Kind() == reflect.Ptr
	if fieldIsPtr {
		fieldType = fieldType.Elem()
	}

	subIndex := hh.Index()
	sliceLen := sourceValue.Len()
	itemSize := 0

	switch fieldType.Kind() {
	case reflect.Struct:
		for i := 0; i < sliceLen; i++ {
			fieldValue := sourceValue.Index(i)
			if fieldIsPtr {
				fieldValue = fieldValue.Elem()
			}

			err := d.buildRootFromStruct(fieldType, fieldValue, hh, idt+2)
			if err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		itemType := fieldType.Elem()
		if itemType == byteType {
			for i := 0; i < sliceLen; i++ {
				sliceSubIndex := hh.Index()

				fieldValue := sourceValue.Index(i)
				if fieldIsPtr {
					fieldValue = fieldValue.Elem()
				}

				fieldBytes := fieldValue.Bytes()
				byteLen := uint64(len(fieldBytes))

				// we might need to merkelize the child array too.
				// check if we have size hints
				if len(maxSizeHints) > 1 {
					hh.AppendBytes32(fieldBytes)
					hh.MerkleizeWithMixin(sliceSubIndex, byteLen, (maxSizeHints[1].size+31)/32)
				} else {
					hh.PutBytes(fieldBytes)
				}
			}
		} else {
			return fmt.Errorf("non-byte slice/array in slice: %v", itemType.Name())
		}
	case reflect.Uint8:
		if isArray {
			hh.PutBytes(sourceValue.Bytes())
			return nil
		}

		hh.Append(sourceValue.Bytes())
		hh.FillUpTo32()
		itemSize = 1
	case reflect.Uint64:
		for i := 0; i < sliceLen; i++ {
			fieldValue := sourceValue.Index(i)
			if fieldIsPtr {
				fieldValue = fieldValue.Elem()
			}

			hh.AppendUint64(uint64(fieldValue.Uint()))
		}
		itemSize = 8
	}

	if len(maxSizeHints) > 0 {
		var limit uint64
		if itemSize > 0 {
			limit = calculateLimit(maxSizeHints[0].size, uint64(sliceLen), uint64(itemSize))
		} else {
			limit = maxSizeHints[0].size
		}
		hh.MerkleizeWithMixin(subIndex, uint64(sliceLen), limit)
	} else {
		hh.Merkleize(subIndex)
	}

	return nil
}
