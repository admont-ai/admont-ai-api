from optimum.onnxruntime import ORTModelForFeatureExtraction

model = ORTModelForFeatureExtraction.from_pretrained(
    "sentence-transformers/all-MiniLM-L6-v2", export=True
)
model.save_pretrained("./models")
