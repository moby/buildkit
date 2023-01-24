package options

// DagPutSettings is a set of DagPut options.
type DagPutSettings struct {
	InputCodec string
	StoreCodec string
	Pin        string
	Hash       string
}

// DagPutOption is a single DagPut option.
type DagPutOption func(opts *DagPutSettings) error

// DagPutOptions applies the given options to a DagPutSettings instance.
func DagPutOptions(opts ...DagPutOption) (*DagPutSettings, error) {
	options := &DagPutSettings{
		InputCodec: "dag-json",
		StoreCodec: "dag-cbor",
		Pin:        "false",
		Hash:       "sha2-256",
	}

	for _, opt := range opts {
		err := opt(options)
		if err != nil {
			return nil, err
		}
	}
	return options, nil
}

type dagOpts struct{}

var Dag dagOpts

// Pin is an option for Dag.Put which specifies whether to pin the added
// dags. Default is "false".
func (dagOpts) Pin(pin string) DagPutOption {
	return func(opts *DagPutSettings) error {
		opts.Pin = pin
		return nil
	}
}

// InputCodec is an option for Dag.Put which specifies the input encoding of the
// data. Default is "dag-json".
func (dagOpts) InputCodec(codec string) DagPutOption {
	return func(opts *DagPutSettings) error {
		opts.InputCodec = codec
		return nil
	}
}

// StoreCodec is an option for Dag.Put which specifies the codec that the stored
// object will be encoded with. Default is "dag-cbor".
func (dagOpts) StoreCodec(codec string) DagPutOption {
	return func(opts *DagPutSettings) error {
		opts.StoreCodec = codec
		return nil
	}
}

// Hash is an option for Dag.Put which specifies the hash function to use
func (dagOpts) Hash(hash string) DagPutOption {
	return func(opts *DagPutSettings) error {
		opts.Hash = hash
		return nil
	}
}
