package i18n

// catalogue is the set of languages with translations. English is absent on
// purpose: every message id is already the English text, so an English "lookup"
// would be an identity map with a copy of every message in it.
var catalogue = map[Language]map[string]string{
	Chinese: chinese,
}
