package feed

var sponsorBlockCategories = []string{
	"sponsor",
	"intro",
	"outro",
	"interaction",
	"selfpromo",
	"music_offtopic",
	"preview",
	"filler",
}

func ValidSponsorBlockCategories() []string {
	result := make([]string, len(sponsorBlockCategories))
	copy(result, sponsorBlockCategories)
	return result
}
