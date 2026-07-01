package pages

func modalDialogClass(sizeClass, maxHeight, aspectRatio string) string {
	if sizeClass == "" {
		return "w-full max-w-[90vw] " + maxHeight + " " + aspectRatio + " border border-border rounded-lg bg-white shadow-xl flex flex-col"
	}
	return "w-full " + sizeClass + " " + maxHeight + " " + aspectRatio + " border border-border rounded-lg bg-white shadow-xl flex flex-col"
}
