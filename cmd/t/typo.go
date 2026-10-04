package main

import "strings"

// typoCommands are the commands a one-word capture is compared with.
// "add" and "rm" are left out: they are so short that too many real words
// ("bad", "arm") are one letter away from them.
var typoCommands = []string{"list", "focus", "done", "skip", "undo", "edit", "help", "hook", "prompt"}

// likelyTypo reports whether word is probably a mistyped command, such as
// "lsit" for "list", and which one. Only lower-case words are checked, so
// "Done" is still captured as a task.
func likelyTypo(word string) (command string, ok bool) {
	if word != strings.ToLower(word) {
		return "", false
	}
	for _, c := range typoCommands {
		if editDistance(word, c) == 1 {
			return c, true
		}
	}
	return "", false
}

// editDistance counts the single-letter edits needed to turn a into b:
// inserting, deleting or replacing a letter, or swapping two neighbours
// ("lsit" -> "list" is one swap). This is the "optimal string alignment"
// distance, computed with the usual table: d[i][j] is the distance between
// the first i letters of a and the first j letters of b.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(
				d[i-1][j]+1,      // delete
				d[i][j-1]+1,      // insert
				d[i-1][j-1]+cost, // replace (or keep)
			)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1) // swap neighbours
			}
		}
	}
	return d[len(x)][len(y)]
}
