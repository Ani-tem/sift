// sift-extract prints the text of a PDF or image to stdout using the
// frameworks macOS ships with: PDFKit for PDFs, Vision for OCR.
//
//   sift-extract pdf <path>
//   sift-extract ocr <path>
import Foundation
import PDFKit
import Vision

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
    exit(1)
}

let args = CommandLine.arguments
guard args.count == 3 else { fail("usage: sift-extract pdf|ocr <path>") }
let url = URL(fileURLWithPath: args[2])

switch args[1] {
case "pdf":
    guard let doc = PDFDocument(url: url) else { fail("cannot open pdf") }
    if doc.isLocked { fail("pdf is encrypted") }
    print(doc.string ?? "")

case "ocr":
    let req = VNRecognizeTextRequest()
    req.recognitionLevel = .accurate
    req.usesLanguageCorrection = true
    do {
        try VNImageRequestHandler(url: url).perform([req])
    } catch {
        fail("ocr: \(error.localizedDescription)")
    }
    for obs in req.results ?? [] {
        if let line = obs.topCandidates(1).first { print(line.string) }
    }

default:
    fail("unknown mode \(args[1])")
}
