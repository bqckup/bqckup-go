# Schema-v2 change rules

The canonical source is [`docs/configuration-v2.md`](../../../../docs/configuration-v2.md). Read it and current loader/validation tests before editing YAML types.

1. Give every field an exact typed representation with matching `mapstructure` and `yaml` tags.
2. Preserve one fresh Viper instance per file and `UnmarshalExact`; never add arbitrary maps to tolerate unknown fields.
3. Specify applicable type, required/optional behavior, default, normalization, validation, runtime effect, and security sensitivity.
4. Do not accept a field until its behavior works. An accepted-but-unused field is a broken public contract.
5. Keep schema v2 only for compatible additions with stable omitted behavior. Changed meaning, required fields, or incompatible defaults require a version/migration decision.
6. Store secret references as environment-variable names and resolve them only at runtime. Validation never contacts providers.
7. Update config structs, strict-load/validation tests, adapter behavior tests, `configs/`, the `init` template, and canonical docs together.

Older binaries reject new fields because decoding is strict. Document upgrade ordering when adding an optional field.
